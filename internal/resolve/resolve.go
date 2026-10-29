// Package resolve turns bojo.toml plus remote repositories into a flat graph:
// every package, its import routes, its linker flags and the hooks it wants.
package resolve

import (
	"fmt"
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
	Name   string
	Repo   string // normalised URL, empty for in-repo packages of a local tree
	Commit string
	Tag    string
	Tree   string
	Dir    string

	Routes []string
	Kind   string
	Entry  string
	Linker pkg.Linker
	Hooks  map[string][][]string
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

// Resolver carries the cache, the locked trees and reports progress.
type Resolver struct {
	Store   *cache.Store
	Locked  map[string]string // repo -> "git-tree:<oid>" from bojo.lock
	Verbose bool
}

// Resolve walks the dependency graph starting from the project manifest.
func (r *Resolver) Resolve(cfg *manifest.File) (*Graph, error) {
	g := &Graph{Packages: map[string]*Package{}}

	for name, d := range cfg.Deps {
		return nil, fmt.Errorf("local dependency %q (deps.%s.path = %q) is not implemented yet; use a repository URL", name, name, d.Path)
	}

	// Breadth-first work list of (repo, ref, libs, alias).
	type job struct {
		repo   string
		tag    string
		commit string
		libs   []string
		alias  map[string]string
	}
	var queue []job
	for _, d := range cfg.DepsFrom {
		queue = append(queue, job{repo: d.Repo, tag: d.Tag, commit: d.Commit, libs: d.Libs, alias: d.Alias})
	}

	seenRepo := map[string]string{} // repo@commit -> checkout dir

	for len(queue) > 0 {
		j := queue[0]
		queue = queue[1:]

		norm, err := urlkey.Normalize(j.repo)
		if err != nil {
			return nil, err
		}

		var tagName, commit, wantTree string
		if j.commit != "" {
			_, commit, err = r.Store.ResolveRef(norm, "", j.commit)
			if err != nil {
				return nil, err
			}
		} else {
			pkgForTag := ""
			if len(j.libs) == 1 {
				pkgForTag = j.libs[0]
			}
			if pkgForTag == "" {
				return nil, fmt.Errorf("%s: a tag reference needs exactly one package in libs (tags are per package)", norm)
			}
			// bojo.toml stores the full tag (`pkg/vX.Y.Z`); the resolver works on
			// the version part, so strip the package prefix when present.
			ref := strings.TrimPrefix(j.tag, pkgForTag+"/")
			tagName, commit, err = r.Store.ResolveRef(norm, pkgForTag, ref)
			if err != nil {
				return nil, err
			}
		}

		key := norm + "@" + commit
		dir, ok := seenRepo[key]
		if !ok {
			wantTree = r.Locked[norm]
			dir, err = r.Store.Ensure(norm, commit, wantTree)
			if err != nil {
				return nil, err
			}
			seenRepo[key] = dir
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
			if err := g.add(Package{
				Name:   p.Name,
				Repo:   norm,
				Commit: commit,
				Tag:    tagName,
				Tree:   tree,
				Dir:    p.Dir,
				Routes: p.Modules,
				Kind:   p.Kind,
				Entry:  p.Entry,
				Linker: p.Linker,
				Hooks:  p.Hooks,
			}, j.alias); err != nil {
				return nil, err
			}

			// in-repo dependencies
			for depName, d := range p.Deps {
				if d.InRepo == "" && d.Path == "" {
					return nil, fmt.Errorf("%s: package %q: dependency %q needs in-repo or path", norm, p.Name, depName)
				}
				if d.Path != "" {
					return nil, fmt.Errorf("%s: package %q: path dependency %q is not implemented yet", norm, p.Name, depName)
				}
				dp, ok := pt.Packages[d.InRepo]
				if !ok {
					return nil, fmt.Errorf("%s: package %q: in-repo dependency %q not found", norm, p.Name, d.InRepo)
				}
				if err := g.add(Package{
					Name:   dp.Name,
					Repo:   norm,
					Commit: commit,
					Tag:    tagName,
					Tree:   tree,
					Dir:    dp.Dir,
					Routes: dp.Modules,
					Kind:   dp.Kind,
					Entry:  dp.Entry,
					Linker: dp.Linker,
					Hooks:  dp.Hooks,
				}, nil); err != nil {
					return nil, err
				}
			}

			// external dependencies
			for _, d := range p.DepsFrom {
				queue = append(queue, job{repo: d.Repo, tag: d.Tag, commit: d.Commit, libs: d.Libs, alias: d.Alias})
			}
		}
	}

	sort.Slice(g.Routes, func(i, j int) bool { return g.Routes[i].Path < g.Routes[j].Path })
	sort.Slice(g.Lock, func(i, j int) bool { return g.Lock[i].Name < g.Lock[j].Name })
	return g, nil
}

// add registers a package, applies aliases and checks route conflicts (R1/R2).
func (g *Graph) add(p Package, alias map[string]string) error {
	if _, exists := g.Packages[p.Name]; exists {
		return nil // already merged; the graph is a set
	}
	g.Packages[p.Name] = &p

	routes := p.Routes
	if a, ok := alias[p.Name]; ok && a != "" {
		if len(routes) != 1 {
			return fmt.Errorf("package %q: alias is only allowed for a package with exactly one route (has %d)", p.Name, len(routes))
		}
		if !strings.HasPrefix(routes[0], p.Name) {
			return fmt.Errorf("package %q: alias needs a route starting with the package name, got %q", p.Name, routes[0])
		}
		routes = []string{a + strings.TrimPrefix(routes[0], p.Name)}
	}

	for _, route := range routes {
		route = strings.Trim(route, "/")
		if route == "" {
			return fmt.Errorf("package %q has an empty route", p.Name)
		}
		for _, existing := range g.Routes {
			if strings.EqualFold(existing.Path, route) {
				return fmt.Errorf("conflict: route %q provided by %q and %q", route, existing.Pkg, p.Name)
			}
		}
		g.Routes = append(g.Routes, Route{Path: route, Dir: p.Dir, Pkg: p.Name})
	}

	for hookName, commands := range p.Hooks {
		for _, cmd := range commands {
			if len(cmd) == 0 {
				continue
			}
			g.Hooks = append(g.Hooks, hooks.Requirement{
				Repo:    p.Repo,
				Commit:  p.Commit,
				Pkg:     p.Name,
				Hook:    hookName,
				Command: cmd,
			})
		}
	}

	aliasName := alias[p.Name]
	g.Lock = append(g.Lock, LockEntry{
		Name:   p.Name,
		Repo:   p.Repo,
		Tag:    p.Tag,
		Commit: p.Commit,
		Tree:   "git-tree:" + p.Tree,
		Alias:  aliasName,
	})
	return nil
}

// ModulesDir is where the import routes are materialised.
func ModulesDir(projectDir string) string {
	return filepath.Join(projectDir, ".bojo", "modules")
}

// LockedCommits returns repo -> commit for the staleness check (D28).
func (g *Graph) LockedCommits() map[string]string {
	out := map[string]string{}
	for _, e := range g.Lock {
		out[e.Repo] = e.Commit
	}
	return out
}
