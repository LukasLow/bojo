// Package pkg parses bojo.pkg.toml: the repository root file (repo metadata plus
// an import list) and one file per package (DESIGN §5.2, D5/D5b/D42).
package pkg

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName is the only accepted name for a package file (D5).
const FileName = "bojo.pkg.toml"

// MojoRange is the compatibility window for a source package.
type MojoRange struct {
	Min string `toml:"min"`
	Max string `toml:"max"`
}

// Repo is the repository-level block.
type Repo struct {
	URL     string    `toml:"url"`
	License string    `toml:"license"`
	Mojo    MojoRange `toml:"mojo"`
}

// Linker is what a consumer will need at link time.
type Linker struct {
	Libs    []string `toml:"libs"`
	LibDirs []string `toml:"libdirs"`
	Runtime []string `toml:"runtime"`
}

// Dep is a local dependency: another package in the same repository.
type Dep struct {
	InRepo string `toml:"in-repo"`
	Path   string `toml:"path"`
}

// DepFrom is one repository at one version.
type DepFrom struct {
	Repo   string            `toml:"repo"`
	Tag    string            `toml:"tag"`
	Commit string            `toml:"commit"`
	Libs   []string          `toml:"libs"`
	Alias  map[string]string `toml:"alias"`
}

// Pkg is one package declared in a package file.
type Pkg struct {
	Kind     string                `toml:"kind"` // "" (library) or "cmd"
	Entry    string                `toml:"entry"`
	Modules  []string              `toml:"modules"`
	Deps     map[string]Dep        `toml:"deps"`
	DepsFrom []DepFrom             `toml:"deps-from"`
	Linker   Linker                `toml:"linker"`
	Hooks    map[string][][]string `toml:"hooks"`
}

// File is the parsed shape of one bojo.pkg.toml.
type File struct {
	Repo   Repo           `toml:"repo"`
	Import []string       `toml:"import"`
	Pkg    map[string]Pkg `toml:"pkg"`
}

// Package is a package after tree resolution, with the directory it lives in.
type Package struct {
	Name string
	Dir  string
	File string
	Pkg
}

// Tree is the flattened model of a repository.
type Tree struct {
	Root     string
	Repo     Repo
	Packages map[string]Package
}

// LoadTree reads the root package file in dir and follows its import list
// transitively. Duplicate paths are deduplicated before the name check, so a
// diamond import is legal (D5b); a cycle is an error.
func LoadTree(dir string) (*Tree, error) {
	rootFile := filepath.Join(dir, FileName)
	f, err := parseFile(rootFile)
	if err != nil {
		return nil, err
	}
	if f.Repo.URL == "" {
		return nil, fmt.Errorf("%s: [repo] url is required", rootFile)
	}

	tree := &Tree{Root: dir, Repo: f.Repo, Packages: map[string]Package{}}

	seen := map[string]bool{}
	var walk func(path string, stack []string) error
	walk = func(path string, stack []string) error {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		// A cycle is detected on the current stack, so it must be checked before
		// the "already merged" shortcut that makes diamonds legal (D5b).
		for _, s := range stack {
			if s == abs {
				return fmt.Errorf("import cycle: %s", strings.Join(append(stack, abs), " -> "))
			}
		}
		if seen[abs] {
			return nil // diamond: already merged
		}
		seen[abs] = true

		var pf *File
		if abs == mustAbs(rootFile) {
			pf = f
		} else {
			pf, err = parseFile(abs)
			if err != nil {
				return err
			}
		}

		for name, p := range pf.Pkg {
			if p.Kind != "" && p.Kind != "cmd" {
				return fmt.Errorf("%s: pkg %q has kind %q (want \"cmd\" or empty)", abs, name, p.Kind)
			}
			if p.Kind == "cmd" && p.Entry == "" {
				return fmt.Errorf("%s: cmd %q needs an entry", abs, name)
			}
			if p.Kind == "" && len(p.Modules) == 0 {
				return fmt.Errorf("%s: library %q needs at least one modules route", abs, name)
			}
			if existing, ok := tree.Packages[name]; ok {
				return fmt.Errorf("package %q declared twice: %s and %s", name, existing.File, abs)
			}
			tree.Packages[name] = Package{Name: name, Dir: filepath.Dir(abs), File: abs, Pkg: p}
		}

		next := append(append([]string{}, stack...), abs)
		for _, imp := range pf.Import {
			if strings.Contains(imp, "..") {
				return fmt.Errorf("%s: import %q must not contain \"..\"", abs, imp)
			}
			target := filepath.Join(tree.Root, imp, FileName)
			if _, err := os.Stat(target); err != nil {
				return fmt.Errorf("%s: import %q has no %s", abs, imp, FileName)
			}
			if err := walk(target, next); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(rootFile, nil); err != nil {
		return nil, err
	}
	if len(tree.Packages) == 0 {
		return nil, fmt.Errorf("%s: no [pkg.*] entries", rootFile)
	}
	return tree, nil
}

// Names returns the package names in sorted order.
func (t *Tree) Names() []string {
	names := make([]string, 0, len(t.Packages))
	for n := range t.Packages {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func parseFile(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	md, err := toml.Decode(string(raw), &f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("%s: unknown keys: %v", path, u)
	}
	return &f, nil
}
