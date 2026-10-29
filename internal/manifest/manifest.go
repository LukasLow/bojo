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

type Build struct {
	Output string `toml:"output"`
	Strip  bool   `toml:"strip"`
}

// Dep is a local dependency: a directory relative to the project root.
type Dep struct {
	Path string `toml:"path"`
}

// DepFrom is one repository at one version, providing one or more packages.
type DepFrom struct {
	Repo   string            `toml:"repo"`
	Tag    string            `toml:"tag"`
	Commit string            `toml:"commit"`
	Libs   []string          `toml:"libs"`
	Alias  map[string]string `toml:"alias"`
}

type File struct {
	Project  Project               `toml:"project"`
	Version  Version               `toml:"version"`
	Build    Build                 `toml:"build"`
	DepsFrom []DepFrom             `toml:"deps-from"`
	Hooks    map[string][][]string `toml:"hooks"`

	// Deps holds `[deps.<name>]` (local path) and `[deps.python]` (Python
	// packages) in one TOML table, so it is decoded in two passes.
	depsRaw    map[string]toml.Primitive `toml:"deps"`
	Local      map[string]Dep
	PythonDeps map[string]string
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
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("%s: unknown keys: %v", path, undecoded)
	}

	f.Local = map[string]Dep{}
	f.PythonDeps = map[string]string{}
	for name, prim := range f.depsRaw {
		if name == "python" {
			if err := md.PrimitiveDecode(prim, &f.PythonDeps); err != nil {
				return nil, fmt.Errorf("%s: deps.python: %w", path, err)
			}
			continue
		}
		var d Dep
		if err := md.PrimitiveDecode(prim, &d); err != nil {
			return nil, fmt.Errorf("%s: deps.%s: %w", path, name, err)
		}
		f.Local[name] = d
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
	if len(f.PythonDeps) > 0 && f.Version.Python == "" {
		return nil, fmt.Errorf("%s: deps.python needs [version] python", path)
	}

	for i, d := range f.DepsFrom {
		if d.Repo == "" {
			return nil, fmt.Errorf("%s: deps-from[%d] needs a repo", path, i)
		}
		if d.Tag == "" && d.Commit == "" {
			return nil, fmt.Errorf("%s: deps-from[%d] (%s) needs a tag or a commit", path, i, d.Repo)
		}
		if d.Tag != "" && d.Commit != "" {
			return nil, fmt.Errorf("%s: deps-from[%d] (%s) has both tag and commit; pick one", path, i, d.Repo)
		}
		if len(d.Libs) == 0 {
			return nil, fmt.Errorf("%s: deps-from[%d] (%s) needs at least one entry in libs", path, i, d.Repo)
		}
	}

	return &f, nil
}
