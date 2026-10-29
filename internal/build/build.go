// Package build materialises the module root and drives `mojo build`
// (DESIGN §10/§11).
package build

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/LukasLow/bojo/internal/resolve"
)

// Materialize builds `.bojo/modules` from the resolved routes.
//
// The new tree is assembled next to the live one and swapped in with a rename;
// a reader never observes a half-built module root (D26a).
func Materialize(projectDir string, routes []resolve.Route) (string, error) {
	dir := resolve.ModulesDir(projectDir)
	tmp := dir + ".new"
	old := dir + ".old"

	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	for _, r := range routes {
		target := filepath.Join(tmp, filepath.FromSlash(r.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := os.Symlink(r.Dir, target); err != nil {
			return "", fmt.Errorf("route %q -> %s: %w", r.Path, r.Dir, err)
		}
	}

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
	args := []string{
		"build", entry,
		"-I", modulesDir,
		"-I", ".",
		"-o", out,
	}
	args = append(args, extra...)

	cmd := exec.Command("mojo", args...)
	cmd.Dir = projectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"MODULAR_CACHE_DIR="+filepath.Join(projectDir, ".bojo", "mojo-cache"),
	)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

// Strip reduces a produced binary in place when strip is enabled.
func Strip(path string) error {
	if _, err := exec.LookPath("strip"); err != nil {
		return nil // not available: not an error
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
