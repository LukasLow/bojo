// Package cache owns the project-local object cache (DESIGN §8, D32).
//
// Every write is atomic: work in a temporary directory, then rename into place
// (D26). Fetching verifies the git tree against the lock before the checkout
// becomes visible; afterwards the tree is intentionally writable (D33).
package cache

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/LukasLow/bojo/internal/tag"
	"github.com/LukasLow/bojo/internal/urlkey"
)

// FetchError marks a failure that came from talking to the remote (network,
// authentication, missing ref). Callers map it to the network exit code.
type FetchError struct{ Err error }

func (e *FetchError) Error() string { return e.Err.Error() }
func (e *FetchError) Unwrap() error { return e.Err }

// Store is the cache of one project.
type Store struct {
	root string // <project>/.bojo/cache
}

// New returns the cache for a project directory.
func New(projectDir string) *Store {
	return &Store{root: filepath.Join(projectDir, ".bojo", "cache")}
}

// Root is the cache directory, useful for diagnostics.
func (s *Store) Root() string { return s.root }

// Mojo is the subtree for Mojo dependencies (git checkouts, module root and
// the compiler cache). Python environments live next to it, under python/
// (D55).
func (s *Store) Mojo() string { return filepath.Join(s.root, "mojo") }

// Modules is the materialised module root handed to -I.
func (s *Store) Modules() string { return filepath.Join(s.Mojo(), "modules") }

// Compiler is the compiler's own scratch cache (MODULAR_CACHE_DIR).
func (s *Store) Compiler() string { return filepath.Join(s.Mojo(), "compiler") }

// PythonEnv is the prepared environment for one (version, package set).
func (s *Store) PythonEnv(version, key string) string {
	return filepath.Join(s.root, "python", version, key)
}

// Src returns the checkout directory for one repository at one commit.
func (s *Store) Src(repo, tag, commit string) (string, error) {
	norm, err := urlkey.Normalize(repo)
	if err != nil {
		return "", err
	}
	path, err := urlkey.Path(norm)
	if err != nil {
		return "", err
	}
	// The snapshot is the commit only: one checkout per commit, no matter how
	// many tags point at it. Tags are for people, the hash is for the machine
	// (D63).
	return filepath.Join(s.Mojo(), "src", filepath.FromSlash(path)+"@"+short(commit)), nil
}

// Tags lists the tags of a remote repository without cloning it.
func (s *Store) Tags(repo string) ([]string, error) {
	norm, err := urlkey.Normalize(repo)
	if err != nil {
		return nil, err
	}
	out, err := run("", "git", "ls-remote", "--tags", "--refs", norm)
	if err != nil {
		return nil, &FetchError{Err: fmt.Errorf("listing tags of %s: %w", norm, err)}
	}
	var tags []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		tags = append(tags, strings.TrimPrefix(fields[1], "refs/tags/"))
	}
	return tags, nil
}

// ResolveRef turns "latest", "v0.0.1" or a commit prefix into a concrete tag
// (possibly empty) and commit.
func (s *Store) ResolveRef(repo, pkgName, ref string) (tagName, commit string, err error) {
	if isHexPrefix(ref) {
		commit, err = s.resolveCommit(repo, ref)
		return "", commit, err
	}
	tags, err := s.Tags(repo)
	if err != nil {
		return "", "", err
	}
	v, err := tag.Match(pkgName, ref, tags)
	if err != nil {
		return "", "", err
	}
	tagName = pkgName + "/" + v.String()
	commit, err = s.commitOfTag(repo, tagName)
	if err != nil {
		return "", "", err
	}
	return tagName, commit, nil
}

func (s *Store) commitOfTag(repo, tagName string) (string, error) {
	norm, err := urlkey.Normalize(repo)
	if err != nil {
		return "", err
	}
	out, err := run("", "git", "ls-remote", norm, "refs/tags/"+tagName, "refs/tags/"+tagName+"^{}")
	if err != nil {
		return "", &FetchError{Err: err}
	}
	return pickCommit(out, tagName)
}

func (s *Store) resolveCommit(repo, prefix string) (string, error) {
	norm, err := urlkey.Normalize(repo)
	if err != nil {
		return "", err
	}
	out, err := run("", "git", "ls-remote", norm)
	if err != nil {
		return "", &FetchError{Err: err}
	}
	var matches []string
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sha := fields[0]
		if strings.HasPrefix(sha, prefix) && !seen[sha] {
			seen[sha] = true
			matches = append(matches, sha)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no commit starting with %q in %s", prefix, norm)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("commit prefix %q is ambiguous in %s", prefix, norm)
	}
}

// Ensure makes sure the checkout for (repo, commit) exists and returns its
// directory. When wantTree is non-empty, the tree OID is verified.
func (s *Store) Ensure(repo, tag, commit, wantTree string) (string, error) {
	dir, err := s.Src(repo, tag, commit)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}

	norm, err := urlkey.Normalize(repo)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".fetch-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	if _, err := run(tmp, "git", "init", "--quiet"); err != nil {
		return "", err
	}
	if _, err := run(tmp, "git", "remote", "add", "origin", norm); err != nil {
		return "", err
	}
	if _, err := run(tmp, "git", "fetch", "--quiet", "--depth", "1", "origin", commit); err != nil {
		return "", &FetchError{Err: fmt.Errorf("fetching %s@%s: %w", norm, short(commit), err)}
	}
	if _, err := run(tmp, "git", "checkout", "--quiet", "FETCH_HEAD"); err != nil {
		return "", err
	}

	tree, err := run(tmp, "git", "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	tree = strings.TrimSpace(tree)
	if wantTree != "" && !strings.HasPrefix(wantTree, tree) && !strings.HasPrefix(tree, strings.TrimPrefix(wantTree, "git-tree:")) {
		return "", fmt.Errorf("tree mismatch for %s@%s: lock says %s, checkout has %s", norm, short(commit), wantTree, tree)
	}

	if err := os.Rename(tmp, dir); err != nil {
		// Another process may have won the race; that is fine.
		if _, statErr := os.Stat(dir); statErr == nil {
			return dir, nil
		}
		return "", err
	}
	return dir, nil
}

// TreeOf returns the git tree OID of a checkout.
func (s *Store) TreeOf(dir string) (string, error) {
	out, err := run(dir, "git", "rev-parse", "HEAD^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// VerifyFresh clones the pinned commit into a temporary directory and compares
// its tree OID with the lock (D36). It never touches the project cache, so a
// hook that mutated the working tree cannot hide a mismatch.
func (s *Store) VerifyFresh(repo, commit, wantTree string) error {
	norm, err := urlkey.Normalize(repo)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "bojo-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if _, err := run(tmp, "git", "init", "--quiet"); err != nil {
		return err
	}
	if _, err := run(tmp, "git", "remote", "add", "origin", norm); err != nil {
		return err
	}
	if _, err := run(tmp, "git", "fetch", "--quiet", "--depth", "1", "origin", commit); err != nil {
		return &FetchError{Err: fmt.Errorf("fetching %s@%s: %w", norm, short(commit), err)}
	}
	if _, err := run(tmp, "git", "checkout", "--quiet", "FETCH_HEAD"); err != nil {
		return err
	}
	tree, err := s.TreeOf(tmp)
	if err != nil {
		return err
	}
	want := strings.TrimPrefix(wantTree, "git-tree:")
	if want != "" && want != tree {
		return fmt.Errorf("tree mismatch: lock %s, checkout %s", want, tree)
	}
	return nil
}

func pickCommit(lsRemote, tagName string) (string, error) {
	var direct, peeled string
	for _, line := range strings.Split(strings.TrimSpace(lsRemote), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ref := fields[1]
		switch ref {
		case "refs/tags/" + tagName:
			direct = fields[0]
		case "refs/tags/" + tagName + "^{}":
			peeled = fields[0]
		}
	}
	if peeled != "" {
		return peeled, nil // annotated tag: the commit, not the tag object
	}
	if direct != "" {
		return direct, nil
	}
	return "", fmt.Errorf("tag %q not found", tagName)
}

func isHexPrefix(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			return "", fmt.Errorf("%s: %s", name, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

func asExitError(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}
