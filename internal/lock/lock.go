// Package lock reads and writes bojo.lock (DESIGN §5.3).
package lock

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// FileName is the lock file next to bojo.toml.
const FileName = "bojo.lock"

// Entry is one pinned package.
type Entry struct {
	Name   string `toml:"name"`
	Repo   string `toml:"repo"`
	Tag    string `toml:"tag"`
	Commit string `toml:"commit"`
	Tree   string `toml:"tree"`
	Alias  string `toml:"alias,omitempty"`
}

// PythonPackage pins one wheel: version and the hash that was installed.
//
// The hash lives here and not in bojo.allow, because it is an install artefact,
// not a trust decision: a version that suddenly resolves to a different wheel is
// a supply-chain event and must fail loudly (D48).
type PythonPackage struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	SHA256  string `toml:"sha256"`
}

// Python pins the bundled interpreter and its package set.
type Python struct {
	Version  string          `toml:"version"`
	Packages []PythonPackage `toml:"package"`
}

// File is the whole lock.
type File struct {
	Version  int     `toml:"version"`
	Mojo     string  `toml:"mojo"`
	Packages []Entry `toml:"package"`
	Python   *Python `toml:"python,omitempty"`
}

// Load reads the lock. A missing file is an empty lock.
func Load(dir string) (*File, error) {
	path := filepath.Join(dir, FileName)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &File{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	if err := toml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// Save writes the lock atomically (D26).
func Save(dir string, f *File) error {
	var buf []byte
	w := &writer{}
	if err := toml.NewEncoder(w).Encode(f); err != nil {
		return err
	}
	buf = w.b
	path := filepath.Join(dir, FileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// TreesByCommit returns "<repo>@<commit>" -> tree. Keying by commit is what makes
// a version bump resolvable: the old tree must not be compared against the new
// commit (review blocker).
func (f *File) TreesByCommit() map[string]string {
	out := map[string]string{}
	for _, e := range f.Packages {
		out[e.Repo+"@"+e.Commit] = e.Tree
	}
	return out
}

// writer is a minimal io.Writer collecting bytes.
type writer struct{ b []byte }

func (w *writer) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}
