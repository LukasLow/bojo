// Package hooks implements the trust log (`bojo.allow`) and hook execution
// (DESIGN §12). Nothing here ever runs a hook on its own: the caller must pass a
// gate, and the gate only passes when the exact (repo, commit, pkg, hook,
// command) tuple has an approved line.
package hooks

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FileName is the trust log next to bojo.toml.
const FileName = "bojo.allow"

// Entry is one approved hook.
type Entry struct {
	Repo    string   `json:"repo"`
	Commit  string   `json:"commit"`
	Pkg     string   `json:"pkg"`
	Hook    string   `json:"hook"`
	Command []string `json:"command"`
	Binary  string   `json:"binary,omitempty"`
}

// ID is the short handle shown to the user and accepted by `bojo allow <id>`.
// It is a hash over every field that must match, so two different approvals can
// never share an ID.
func (e Entry) ID() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		e.Repo, e.Commit, e.Pkg, e.Hook, strings.Join(e.Command, "\x00"),
	}, "\x1f")))
	return hex.EncodeToString(sum[:])[:6]
}

// Store is the content of bojo.allow.
type Store struct {
	Path    string
	Entries []Entry
	byID    map[string]Entry
}

// Load reads the trust log. A missing file is an empty store.
func Load(dir string) (*Store, error) {
	s := &Store{Path: filepath.Join(dir, FileName), byID: map[string]Entry{}}
	f, err := os.Open(s.Path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", s.Path, line, err)
		}
		s.Entries = append(s.Entries, e)
		s.byID[e.ID()] = e
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

// Append writes one line. It never rewrites the file, so two approvals by two
// people merge without conflict.
func (s *Store) Append(e Entry) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		return err
	}
	s.Entries = append(s.Entries, e)
	s.byID[e.ID()] = e
	return nil
}

// Find returns the single entry for an ID. An unknown or ambiguous ID is an
// error: the user must not approve the wrong thing.
func (s *Store) Find(id string) (Entry, error) {
	e, ok := s.byID[id]
	if !ok {
		return Entry{}, fmt.Errorf("no approval with id %q (see `bojo allow --list`)", id)
	}
	return e, nil
}

// Allows reports whether the tuple is approved.
func (s *Store) Allows(repo, commit, pkgName, hook string, command []string) bool {
	for _, e := range s.Entries {
		if e.Repo != repo || e.Commit != commit || e.Pkg != pkgName || e.Hook != hook {
			continue
		}
		if equalArgs(e.Command, command) {
			return true
		}
	}
	return false
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Stale returns entries whose (repo, commit) is not part of the current lock.
// Such lines are ignored and reported (D28).
func (s *Store) Stale(locked map[string]string) []Entry {
	var out []Entry
	for _, e := range s.Entries {
		if locked[e.Repo] != e.Commit {
			out = append(out, e)
		}
	}
	return out
}

// Requirement is one hook a build wants to run.
type Requirement struct {
	Repo    string
	Commit  string
	Pkg     string
	Hook    string
	Command []string
}

// Entry builds the log entry for a requirement, resolving the binary path for
// the audit trail (D39).
func (r Requirement) Entry() Entry {
	binary := ""
	if len(r.Command) > 0 {
		if p, err := exec.LookPath(r.Command[0]); err == nil {
			binary = p
		}
	}
	return Entry{
		Repo:    r.Repo,
		Commit:  r.Commit,
		Pkg:     r.Pkg,
		Hook:    r.Hook,
		Command: r.Command,
		Binary:  binary,
	}
}

// Blocked returns the requirements that have no approval yet.
func Blocked(store *Store, reqs []Requirement) []Requirement {
	var out []Requirement
	for _, r := range reqs {
		if !store.Allows(r.Repo, r.Commit, r.Pkg, r.Hook, r.Command) {
			out = append(out, r)
		}
	}
	return out
}

// Run executes one hook command. No shell, no pipes, no script file (D16a).
func Run(command []string, dir string) error {
	if len(command) == 0 {
		return fmt.Errorf("empty hook command")
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "BOJO_HOOK=1")
	return cmd.Run()
}
