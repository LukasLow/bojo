// Package resolve turns bojo.toml plus remote repositories into a flat graph:
// every package, its import routes, its linker flags and the hooks it wants.
package resolve

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LukasLow/bojo/internal/cache"
	"github.com/LukasLow/bojo/internal/hooks"
	"github.com/LukasLow/bojo/internal/manifest"
	"github.com/LukasLow/bojo/internal/pkg"
	"github.com/LukasLow/bojo/internal/urlkey"
)

// Package is one resolved package.
type Package struct {
	Name string
	// Path is the import path, host.owner.repo.package (D65).
	Path   string
	Alias  string
	Repo   string // normalised URL
	Commit string
	Tag    string
	Tree   string
	Dir    string

	Routes []string
	// DeclaredRoutes are the routes as authored, before any alias (used when
	// rewriting imports in a vendor copy).
	DeclaredRoutes []string
	// Deps are the package names this package depends on.
	Deps []string
	// Direct marks a package the project asked for itself.
	Direct  bool
	Kind    string
	Entry   string
	Linker  pkg.Linker
	Exclude []string
	Hooks   map[string][][]string
}

// Route maps an import route to a directory.
type Route struct {
	Path string
	Dir  string
	Pkg  string
}

// LockEntry is one line of bojo.lock.
type LockEntry struct {
	Name   string
	Repo   string
	Tag    string
	Commit string
	Tree   string
	Alias  string
}

// Graph is the resolved model of a build.
type Graph struct {
	Packages map[string]*Package
	Routes   []Route
	Hooks    []hooks.Requirement
	Lock     []LockEntry
}

// Resolver carries the cache and the trees pinned by bojo.lock.
type Resolver struct {
	Store *cache.Store
	// Locked maps "<repo>@<commit>" to the tree OID from the lock, so a version
	// bump does not get compared against the previous tree (review blocker).
	Locked  map[string]string
	Verbose bool
}

type job struct {
	direct bool
	repo   string
	tag    string
	commit string
	libs   []string
	alias  map[string]string
}

// Resolve walks the dependency graph starting from the project manifest.
func (r *Resolver) Resolve(cfg *manifest.File) (*Graph, error) {
	g := &Graph{Packages: map[string]*Package{}}

	for name, d := range cfg.Deps.Local {
		return nil, fmt.Errorf("local dependency %q (deps.%s.path = %q) is not implemented yet; use a repository URL", name, name, d.Path)
	}

	var queue []job
	for _, d := range cfg.DepsFrom {
		queue = append(queue, job{direct: true, repo: d.Repo, tag: d.Tag, commit: d.Commit, libs: d.Libs, alias: d.Alias})
	}

	fetched := map[string]string{} // "<repo>@<commit>" -> checkout dir
	expanded := map[string]bool{}  // repository already walked for nested deps
	processed := map[string]bool{} // "<repo>@<commit>/<libs>" already merged

	for len(queue) > 0 {
		j := queue[0]
		queue = queue[1:]

		norm, err := urlkey.Normalize(j.repo)
		if err != nil {
			return nil, err
		}

		var tagName, commit string
		if j.commit != "" {
			_, commit, err = r.Store.ResolveRef(norm, "", j.commit)
		} else {
			if len(j.libs) != 1 {
				return nil, fmt.Errorf("%s: a tag reference needs exactly one package in libs (tags are per package)", norm)
			}
			ref := strings.TrimPrefix(j.tag, j.libs[0]+"/")
			tagName, commit, err = r.Store.ResolveRef(norm, j.libs[0], ref)
		}
		if err != nil {
			return nil, err
		}

		key := norm + "@" + commit
		dir, ok := fetched[key]
		if !ok {
			dir, err = r.Store.Ensure(norm, tagName, commit, r.Locked[key])
			if err != nil {
				return nil, err
			}
			fetched[key] = dir
		}

		tree, err := r.Store.TreeOf(dir)
		if err != nil {
			return nil, err
		}
		pt, err := pkg.LoadTree(dir)
		if err != nil {
			return nil, err
		}

		for _, lib := range j.libs {
			p, ok := pt.Packages[lib]
			if !ok {
				return nil, fmt.Errorf("%s: no package %q (have: %s)", norm, lib, strings.Join(pt.Names(), ", "))
			}
			if processed[key+"/"+lib] {
				continue
			}
			processed[key+"/"+lib] = true

			deps := make([]string, 0, len(p.Deps)+len(p.DepsFrom))
			for _, d := range p.Deps {
				if dp, err := urlkey.ModulePath(norm, d.InRepo); err == nil {
					deps = append(deps, dp)
				}
			}
			for _, d := range p.DepsFrom {
				dn, err := urlkey.Normalize(d.Repo)
				if err != nil {
					return nil, err
				}
				for _, lib := range d.Libs {
					if dp, err := urlkey.ModulePath(dn, lib); err == nil {
						deps = append(deps, dp)
					}
				}
			}
			modPath, err := urlkey.ModulePath(norm, p.Name)
			if err != nil {
				return nil, err
			}

			if err := g.add(Package{
				Name: p.Name, Path: modPath, Repo: norm, Commit: commit, Tag: tagName, Tree: tree,
				Dir: p.Dir, Routes: p.Modules, DeclaredRoutes: p.Modules, Deps: deps,
				Direct: j.direct, Kind: p.Kind, Entry: p.Entry,
				Linker: p.Linker, Exclude: p.Exclude, Hooks: p.Hooks,
			}, j.alias); err != nil {
				return nil, err
			}

			for depName, d := range p.Deps {
				if d.InRepo == "" {
					return nil, fmt.Errorf("%s: package %q: dependency %q needs in-repo (path dependencies are not implemented yet)", norm, p.Name, depName)
				}
				dp, ok := pt.Packages[d.InRepo]
				if !ok {
					return nil, fmt.Errorf("%s: package %q: in-repo dependency %q not found", norm, p.Name, d.InRepo)
				}
				subDeps := make([]string, 0, len(dp.Deps)+len(dp.DepsFrom))
				for _, d := range dp.Deps {
					if dpath, err := urlkey.ModulePath(norm, d.InRepo); err == nil {
						subDeps = append(subDeps, dpath)
					}
				}
				for _, d := range dp.DepsFrom {
					dn, err := urlkey.Normalize(d.Repo)
					if err != nil {
						return nil, err
					}
					for _, lib := range d.Libs {
						if dpath, err := urlkey.ModulePath(dn, lib); err == nil {
							subDeps = append(subDeps, dpath)
						}
					}
				}
				subPath, err := urlkey.ModulePath(norm, dp.Name)
				if err != nil {
					return nil, err
				}
				if err := g.add(Package{
					Name: dp.Name, Path: subPath, Repo: norm, Commit: commit, Tag: tagName, Tree: tree,
					Dir: dp.Dir, Routes: dp.Modules, DeclaredRoutes: dp.Modules,
					Deps: subDeps, Kind: dp.Kind, Entry: dp.Entry,
					Linker: dp.Linker, Exclude: dp.Exclude, Hooks: dp.Hooks,
				}, nil); err != nil {
					return nil, err
				}
			}
		}

		// A repository's own dependencies are enqueued once, never per package:
		// otherwise A->B->A grows the queue forever (review blocker).
		if !expanded[key] {
			expanded[key] = true
			for _, p := range pt.Packages {
				for _, d := range p.DepsFrom {
					queue = append(queue, job{repo: d.Repo, tag: d.Tag, commit: d.Commit, libs: d.Libs, alias: d.Alias})
				}
			}
		}
	}

	sort.Slice(g.Routes, func(i, j int) bool { return g.Routes[i].Path < g.Routes[j].Path })
	sort.Slice(g.Lock, func(i, j int) bool { return g.Lock[i].Name < g.Lock[j].Name })
	return g, nil
}

// add registers a package, applies aliases and checks route conflicts (R1/R2).
func (g *Graph) add(p Package, alias map[string]string) error {
	if existing, ok := g.Packages[p.Path]; ok {
		if existing.Repo == p.Repo && existing.Commit == p.Commit && existing.Dir == p.Dir {
			return nil // already merged
		}
		return fmt.Errorf("package %q is provided twice: %s@%s and %s@%s (R6)",
			p.Path, existing.Repo, short(existing.Commit), p.Repo, short(p.Commit))
	}
	routes := []string{p.Path}
	if a, ok := alias[p.Name]; ok && a != "" {
		routes = []string{a}
		p.Alias = a
	}
	g.Packages[p.Path] = &p

	for _, route := range routes {
		cleanRoute, err := validateRoute(route)
		if err != nil {
			return fmt.Errorf("package %q: %w", p.Name, err)
		}
		for _, existing := range g.Routes {
			if strings.EqualFold(existing.Path, cleanRoute) {
				return fmt.Errorf("conflict: route %q provided by %q and %q", cleanRoute, existing.Pkg, p.Name)
			}
		}
		g.Routes = append(g.Routes, Route{Path: cleanRoute, Dir: p.Dir, Pkg: p.Name})
	}

	for hookName, commands := range p.Hooks {
		for _, cmd := range commands {
			if len(cmd) == 0 {
				continue
			}
			g.Hooks = append(g.Hooks, hooks.Requirement{
				Repo: p.Repo, Commit: p.Commit, Pkg: p.Name, Hook: hookName, Command: cmd,
			})
		}
	}

	g.Lock = append(g.Lock, LockEntry{
		Name: p.Name, Repo: p.Repo, Tag: p.Tag, Commit: p.Commit,
		Tree: "git-tree:" + p.Tree, Alias: alias[p.Name],
	})
	return nil
}

// validateRoute rejects anything that could escape the module root: absolute
// paths, parent segments, empty segments and backslashes (review blocker).
func validateRoute(route string) (string, error) {
	r := strings.TrimSpace(route)
	if r == "" {
		return "", fmt.Errorf("empty route")
	}
	if strings.ContainsAny(r, "\\:") || strings.HasPrefix(r, "/") {
		return "", fmt.Errorf("route %q must be a relative slash path", route)
	}
	cleaned := path.Clean(r)
	if cleaned == "." || cleaned != strings.Trim(r, "/") {
		return "", fmt.Errorf("route %q is not normalised", route)
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("route %q contains a forbidden segment", route)
		}
	}
	return cleaned, nil
}

// ModulesDir is where the import routes are materialised.
func ModulesDir(projectDir string) string {
	return filepath.Join(projectDir, ".bojo", "cache", "mojo", "modules")
}

// LockedCommits returns repo -> commit for the staleness check (D28).
func (g *Graph) LockedCommits() map[string]string {
	out := map[string]string{}
	for _, e := range g.Lock {
		out[e.Repo] = e.Commit
	}
	return out
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
