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
	Project  Project               `toml:"project"`
	Version  Version               `toml:"version"`
	Build    Build                 `toml:"build"`
	Deps     DepsTable             `toml:"deps"`
	DepsFrom []DepFrom             `toml:"deps-from"`
	Hooks    map[string][][]string `toml:"hooks"`
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
		if strings.HasPrefix(k.String(), "deps.") {
			continue
		}
		unknown = append(unknown, k.String())
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("%s: unknown keys: %v", path, unknown)
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
