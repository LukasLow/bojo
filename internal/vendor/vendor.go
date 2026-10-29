// Package vendor materialises the resolved graph into .vendor/ (D65–D69).
//
// The vendor tree is flat and holds exactly one directory per module path:
//
//	.vendor/github/com/LukasLow/bojo_demo_pkg/cprint/__init__.mojo
//
// One path means one copy, so a package used by several dependents is compiled
// once, and two providers of the same short name never meet. Nothing is
// rewritten: the import path is the identity (D65/D66).
package vendor

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LukasLow/bojo/internal/resolve"
)

const (
	// Dir is the module root handed to the compiler.
	Dir = ".vendor"
	// ManifestName is the provenance file.
	ManifestName = ".vendor.json"
)

// Entry is one vendored package.
type Entry struct {
	Path       string   `json:"path"`
	Dir        string   `json:"dir"`
	Pkg        string   `json:"pkg"`
	Repo       string   `json:"repo"`
	Tag        string   `json:"tag"`
	Commit     string   `json:"commit"`
	RequiredBy []string `json:"required_by,omitempty"`
	Alias      string   `json:"alias,omitempty"`
}

// Manifest is what .vendor/.vendor.json contains.
type Manifest struct {
	Schema  int     `json:"schema"`
	Entries []Entry `json:"entries"`
}

// Roots are the module roots a build must pass to the compiler.
func Roots(projectDir string) []string {
	return []string{filepath.Join(projectDir, Dir)}
}

// Plan lists what Build would produce, without touching the disk.
func Plan(g *resolve.Graph) (Manifest, error) {
	paths := make([]string, 0, len(g.Packages))
	for p := range g.Packages {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	entries := make([]Entry, 0, len(paths))
	seen := map[string]string{}
	for _, key := range paths {
		p := g.Packages[key]
		if prev, ok := seen[p.Path]; ok && prev != p.Commit {
			return Manifest{}, fmt.Errorf("%s is required at two commits: %s… and %s…", p.Path, prev[:8], p.Commit[:8])
		}
		seen[p.Path] = p.Commit
		entries = append(entries, Entry{
			Path: p.Path, Dir: strings.ReplaceAll(p.Path, ".", "/"), Pkg: p.Name,
			Repo: p.Repo, Tag: p.Tag, Commit: p.Commit, Alias: p.Alias,
		})
	}
	return Manifest{Schema: 1, Entries: entries}, nil
}

// Compare reports whether the vendored tree still matches the graph.
func Compare(projectDir string, g *resolve.Graph) error {
	want, err := Plan(g)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(projectDir, Dir, ManifestName))
	if err != nil {
		return fmt.Errorf(".vendor is missing (run `bojo vendor`)")
	}
	var have Manifest
	if err := json.Unmarshal(raw, &have); err != nil {
		return fmt.Errorf(".vendor/.vendor.json is unreadable: %w", err)
	}
	index := map[string]Entry{}
	for _, e := range have.Entries {
		index[e.Path] = e
	}
	for _, w := range want.Entries {
		h, ok := index[w.Path]
		if !ok {
			return fmt.Errorf("%s is missing from .vendor (run `bojo vendor`)", w.Path)
		}
		if h.Commit != w.Commit {
			return fmt.Errorf("%s is at %s…, the graph wants %s… (run `bojo vendor`)", w.Path, h.Commit[:8], w.Commit[:8])
		}
		delete(index, w.Path)
	}
	for path := range index {
		return fmt.Errorf("%s is vendored but no longer required (run `bojo vendor`)", path)
	}
	return nil
}

// Build writes the vendor tree for a resolved graph.
func Build(projectDir string, g *resolve.Graph) (*Manifest, error) {
	tmp, err := os.MkdirTemp(projectDir, ".vendor-tmp-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	paths := make([]string, 0, len(g.Packages))
	for p := range g.Packages {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	byPkg := map[string][]string{}
	for _, p := range g.Packages {
		for _, d := range p.Deps {
			byPkg[d] = append(byPkg[d], p.Path)
		}
	}

	entries := make([]Entry, 0, len(paths))
	seen := map[string]string{} // module path -> commit
	for _, key := range paths {
		p := g.Packages[key]
		if p.Path == "" {
			return nil, fmt.Errorf("package %q has no import path", p.Name)
		}
		if prev, ok := seen[p.Path]; ok && prev != p.Commit {
			return nil, fmt.Errorf("%s is required at two commits: %s… and %s…",
				p.Path, prev[:8], p.Commit[:8])
		}
		seen[p.Path] = p.Commit

		rel := strings.ReplaceAll(p.Path, ".", "/")
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := copyPackage(p.Dir, target, p.Exclude); err != nil {
			return nil, fmt.Errorf("%s: %w", p.Path, err)
		}
		if err := renameModule(target, p.Name, p.Path); err != nil {
			return nil, err
		}

		entries = append(entries, Entry{
			Path: p.Path, Dir: rel, Pkg: p.Name, Repo: p.Repo, Tag: p.Tag,
			Commit: p.Commit, RequiredBy: byPkg[p.Path], Alias: p.Alias,
		})
	}

	if err := writeAliases(tmp, entries); err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	raw, err := json.MarshalIndent(Manifest{Schema: 1, Entries: entries}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, ManifestName), append(raw, '\n'), 0o644); err != nil {
		return nil, err
	}

	final := filepath.Join(projectDir, Dir)
	old := final + ".old"
	if err := os.RemoveAll(old); err != nil {
		return nil, err
	}
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Rename(old, final)
		return nil, err
	}
	_ = os.RemoveAll(old)
	return &Manifest{Schema: 1, Entries: entries}, nil
}

// writeAliases generates a shim package per project alias (D68).
func writeAliases(root string, entries []Entry) error {
	for _, e := range entries {
		if e.Alias == "" {
			continue
		}
		dir := filepath.Join(root, filepath.FromSlash(strings.ReplaceAll(e.Alias, ".", "/")))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		body := fmt.Sprintf("\"\"\"Generated by bojo: %s is %s.\"\"\"\n\nfrom %s import *\n", e.Alias, e.Path, e.Path)
		if err := os.WriteFile(filepath.Join(dir, "__init__.mojo"), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func copyPackage(src, dst string, exclude []string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "target") {
			return filepath.SkipDir
		}
		if matchesAny(filepath.ToSlash(rel), exclude) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode()|0o400)
	})
}

func matchesAny(rel string, patterns []string) bool {
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
		if strings.HasSuffix(pat, "/**") && strings.HasPrefix(rel, strings.TrimSuffix(pat, "**")) {
			return true
		}
		if strings.HasPrefix(pat, "**/") && strings.HasSuffix(rel, strings.TrimPrefix(pat, "**")) {
			return true
		}
	}
	return false
}

// renameModule writes the module path into the vendored manifest, so the copy
// states under which name it is imported.
func renameModule(dir, pkgName, modulePath string) error {
	p := filepath.Join(dir, "bojo.pkg.toml")
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil // a package without a manifest is legal
	}
	out := strings.ReplaceAll(string(raw), `"`+pkgName+`"`, `"`+modulePath+`"`)
	if out != string(raw) {
		return os.WriteFile(p, []byte(out), 0o644)
	}
	return nil
}
