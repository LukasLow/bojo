// Package manifest reads and validates bojo.toml (the consumer side).
//
// Only the fields bojo actually uses are modelled. Unknown fields are an error:
// a manifest that has drifted from the specification must fail loudly instead of
// being silently ignored.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Project struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Entry   string `toml:"entry"`
}

// Version declares what the project is built against (D47). There are no
// ranges: a range is a guess about the future, one version is a fact.
type Version struct {
	Mojo   string `toml:"mojo"`
	Python string `toml:"python"`
}

// Linker declares native libraries the project links against, e.g. a C library
// built by a pre-build hook (D57).
type Linker struct {
	LibDirs []string `toml:"libdirs"`
	Libs    []string `toml:"libs"`
}

type Build struct {
	Output  string   `toml:"output"`
	Strip   bool     `toml:"strip"`
	Include []string `toml:"include"`
}

// Dep is a local dependency: a directory relative to the project root.
type Dep struct {
	Path string `toml:"path"`
}

// DepSpec is one dependency: a version and an optional local alias.
type DepSpec struct {
	Version string
	Alias   string
}

// UnmarshalTOML accepts "v1.2.3" or { version = "v1.2.3", as = "name" }.
func (d *DepSpec) UnmarshalTOML(v any) error {
	switch t := v.(type) {
	case string:
		d.Version = t
	case map[string]any:
		if s, ok := t["version"].(string); ok {
			d.Version = s
		}
		if s, ok := t["as"].(string); ok {
			d.Alias = s
		}
	default:
		return fmt.Errorf("a dependency must be a version string or a table")
	}
	return nil
}

// DepsFrom maps a repository URL to its packages: repo -> package -> spec.
type DepsFrom map[string]map[string]DepSpec

// DepFrom is one repository at one version, providing one or more packages.
type DepFrom struct {
	Repo   string            `toml:"repo"`
	Tag    string            `toml:"tag"`
	Commit string            `toml:"commit"`
	Libs   []string          `toml:"libs"`
	Alias  map[string]string `toml:"alias"`
}

// DepsTable is `[deps]`: local paths under `[deps.<name>]` plus the Python
// package list under `[deps.python]` (D50). Both live in one TOML table, so the
// table is decoded by hand.
type DepsTable struct {
	Local  map[string]Dep
	Python map[string]string
}

// UnmarshalTOML splits the shared table into local and Python entries.
func (d *DepsTable) UnmarshalTOML(v interface{}) error {
	raw, ok := v.(map[string]interface{})
	if !ok {
		return fmt.Errorf("[deps] must be a table")
	}
	d.Local = map[string]Dep{}
	d.Python = map[string]string{}
	for name, entry := range raw {
		table, ok := entry.(map[string]interface{})
		if !ok {
			return fmt.Errorf("[deps.%s] must be a table", name)
		}
		if name == "python" {
			for pkgName, value := range table {
				version, ok := value.(string)
				if !ok {
					return fmt.Errorf("[deps.python] %s must be a string", pkgName)
				}
				d.Python[pkgName] = version
			}
			continue
		}
		path, _ := table["path"].(string)
		d.Local[name] = Dep{Path: path}
	}
	return nil
}

type File struct {
	Schema   int                   `toml:"schema"`
	Project  Project               `toml:"project"`
	Version  Version               `toml:"version"`
	Build    Build                 `toml:"build"`
	Deps     DepsTable             `toml:"deps"`
	DepsFrom DepsFrom              `toml:"deps-from"`
	Hooks    map[string][][]string `toml:"hooks"`
	Alias    map[string]string     `toml:"alias"`
	Linker   Linker                `toml:"linker"`
}

// Load reads bojo.toml from dir and validates it.
func Load(dir string) (*File, error) {
	path := filepath.Join(dir, "bojo.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no bojo.toml in %s (run `bojo init`)", dir)
		}
		return nil, err
	}

	var f File
	md, err := toml.Decode(string(raw), &f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var unknown []string
	for _, k := range md.Undecoded() {
		if strings.HasPrefix(k.String(), "deps.") || strings.HasPrefix(k.String(), "deps-from.") {
			continue // handled by the custom unmarshalers
		}
		unknown = append(unknown, k.String())
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%s: unknown keys: %v", path, unknown)
	}

	if f.Schema != 0 && f.Schema != 1 {
		return nil, fmt.Errorf("%s: unsupported schema %d (this bojo reads schema 1)", path, f.Schema)
	}
	if f.Project.Name == "" {
		return nil, fmt.Errorf("%s: [project] name is required", path)
	}
	if f.Project.Entry == "" {
		return nil, fmt.Errorf("%s: [project] entry is required", path)
	}
	switch f.Build.Output {
	case "", "dir", "onefile":
	default:
		return nil, fmt.Errorf("%s: [build] output must be \"dir\" or \"onefile\", got %q", path, f.Build.Output)
	}
	if len(f.Deps.Python) > 0 && f.Version.Python == "" {
		return nil, fmt.Errorf("%s: deps.python needs [version] python", path)
	}
	for name, d := range f.Deps.Local {
		if d.Path == "" {
			return nil, fmt.Errorf("%s: deps.%s needs a path", path, name)
		}
	}

	for repo, pkgs := range f.DepsFrom {
		if strings.TrimSpace(repo) == "" {
			return nil, fmt.Errorf("%s: a deps-from entry needs a repository", path)
		}
		for name, spec := range pkgs {
			if strings.TrimSpace(name) == "" {
				return nil, fmt.Errorf("%s: deps-from %q has an empty package name", path, repo)
			}
			if n := len(spec.Alias); spec.Alias != "" && (strings.ContainsAny(spec.Alias, " \t/\\:") || n == 0) {
				return nil, fmt.Errorf("%s: alias %q for %s is not a plain name", path, spec.Alias, name)
			}
		}
	}

	return &f, nil
}
