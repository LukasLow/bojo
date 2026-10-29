// Package build materialises the module root and drives `mojo build`
// (DESIGN §10/§11).
package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/LukasLow/bojo/internal/resolve"
)

// Materialize builds `.bojo/modules` from the resolved routes.
//
// The new tree is assembled in a unique temporary directory and swapped in with
// a rename; a fixed `.new` name would race between two concurrent builds, so the
// caller must hold the project lock (Lock).
func Materialize(projectDir string, routes []resolve.Route) (string, error) {
	dir := resolve.ModulesDir(projectDir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "modules-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	for _, r := range routes {
		target := filepath.Join(tmp, filepath.FromSlash(r.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		// Relative links survive moving or copying the project.
		rel, err := filepath.Rel(filepath.Dir(target), r.Dir)
		if err != nil {
			return "", err
		}
		if err := os.Symlink(rel, target); err != nil {
			return "", fmt.Errorf("route %q -> %s: %w", r.Path, r.Dir, err)
		}
	}

	old := dir + ".old"
	if err := os.RemoveAll(old); err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); err == nil {
		if err := os.Rename(dir, old); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.Rename(old, dir) // best effort rollback
		return "", err
	}
	_ = os.RemoveAll(old)
	return dir, nil
}

// Lock takes the project lock. It returns a release function.
//
// The implementation is a PID file with O_EXCL: simple, portable and enough for
// one project on one machine. A stale file from a crashed run is detected by
// checking whether the PID is still alive.
func Lock(projectDir string) (func(), error) {
	path := filepath.Join(projectDir, ".bojo", "lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		raw, readErr := os.ReadFile(path)
		pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if readErr != nil || convErr != nil || !processAlive(pid) {
			_ = os.Remove(path)
			continue
		}
		return nil, fmt.Errorf("another bojo build is running (pid %d); remove %s if that is wrong", pid, path)
	}
	return nil, fmt.Errorf("could not take the project lock %s", path)
}

// processAlive reports whether a PID is still running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// LinkerArgs flattens the `[linker]` sections of every package.
func LinkerArgs(g *resolve.Graph) []string {
	var args []string
	seen := map[string]bool{}
	add := func(flag, value string) {
		key := flag + "=" + value
		if value == "" || seen[key] {
			return
		}
		seen[key] = true
		args = append(args, "-Xlinker", flag+value)
	}
	for _, p := range g.Packages {
		for _, d := range p.Linker.LibDirs {
			add("-L", d)
		}
		for _, l := range p.Linker.Libs {
			add("-l", l)
		}
	}
	return args
}

// BinaryPath is where the build writes its result.
func BinaryPath(projectDir, name string) string {
	return filepath.Join(projectDir, "out", name)
}

// Mojo builds entry into out. ModulesDir must already be materialised.
func Mojo(projectDir, entry, out, modulesDir string, extra []string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	args := []string{"build", entry, "-I", modulesDir, "-I", ".", "-o", out}
	args = append(args, extra...)

	cmd := exec.Command("mojo", args...)
	cmd.Dir = projectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"MODULAR_CACHE_DIR="+filepath.Join(projectDir, ".bojo", "mojo-cache"),
	)
	return cmd.Run()
}

// Strip reduces a produced binary in place.
//
// On macOS stripping invalidates the ad-hoc signature and the binary would be
// killed at exec, so stripping is skipped there until step 10 re-signs (review
// finding on §14).
func Strip(path string) error {
	if runtime.GOOS == "darwin" {
		return nil
	}
	if _, err := exec.LookPath("strip"); err != nil {
		return nil
	}
	return exec.Command("strip", "-s", path).Run()
}

// Describe renders a route list for diagnostics.
func Describe(routes []resolve.Route) string {
	var b strings.Builder
	for _, r := range routes {
		fmt.Fprintf(&b, "  %-24s -> %s\n", r.Path, r.Dir)
	}
	return b.String()
}
