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

type Toolchain struct {
	Mojo   string `toml:"mojo"`
	Target string `toml:"target"`
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
// It is an array of tables because a repository publishes several packages and
// each package carries its own tag.
type DepFrom struct {
	Repo   string            `toml:"repo"`
	Tag    string            `toml:"tag"`
	Commit string            `toml:"commit"`
	Libs   []string          `toml:"libs"`
	Alias  map[string]string `toml:"alias"`
}

type File struct {
	Project   Project               `toml:"project"`
	Toolchain Toolchain             `toml:"toolchain"`
	Build     Build                 `toml:"build"`
	Deps      map[string]Dep        `toml:"deps"`
	DepsFrom  []DepFrom             `toml:"deps-from"`
	Hooks     map[string][][]string `toml:"hooks"`
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
