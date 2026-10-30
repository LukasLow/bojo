// Package build materialises the module root and drives `mojo build`
// (DESIGN §10/§11).
package build

import (
	"bufio"
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

// RuntimeRPATH makes the binary look for its libraries next to itself (D52).
// The flags are passed at link time, because they cannot be added afterwards.
func RuntimeRPATH() []string {
	if runtime.GOOS == "darwin" {
		return []string{"-Xlinker", "-rpath", "-Xlinker", "@loader_path/lib"}
	}
	return []string{"-Xlinker", "-rpath", "-Xlinker", "$ORIGIN/lib"}
}

// systemLibs are provided by the target system and are never bundled.
var systemLibs = map[string]bool{
	"libc.so.6":             true,
	"libm.so.6":             true,
	"libdl.so.2":            true,
	"libpthread.so.0":       true,
	"librt.so.1":            true,
	"libresolv.so.2":        true,
	"libgcc_s.so.1":         true, // part of the base system on glibc targets
	"ld-linux-x86-64.so.2":  true,
	"ld-linux-aarch64.so.1": true,
	"linux-vdso.so.1":       true,
	"libSystem.B.dylib":     true,
}

// Bundle copies the non-system part of the runtime closure next to the binary
// and points the binary at it. It returns the library names that were bundled.
func Bundle(binary string) ([]string, error) {
	libs, err := linkedLibraries(binary)
	if err != nil {
		return nil, err
	}
	libDir := filepath.Join(filepath.Dir(binary), "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		return nil, err
	}

	var bundled []string
	for installName, src := range libs {
		name := filepath.Base(installName)
		if systemLibs[name] || isSystemPath(src) {
			continue
		}
		dst := filepath.Join(libDir, name)
		if err := copyFile(src, dst); err != nil {
			return bundled, fmt.Errorf("bundling %s: %w", name, err)
		}
		if runtime.GOOS == "linux" {
			_ = stripFile(dst)
		}
		bundled = append(bundled, name)
	}

	if runtime.GOOS == "darwin" {
		if err := rewriteMacOS(binary, libs); err != nil {
			return bundled, err
		}
	}
	return bundled, nil
}

// isSystemPath reports whether a library belongs to the OS. Checking the path
// rather than a name allowlist keeps libc++, libobjc and friends out of the
// bundle on macOS.
func isSystemPath(p string) bool {
	return strings.HasPrefix(p, "/usr/lib/") || strings.HasPrefix(p, "/System/")
}

// darwinRPaths reads the LC_RPATH entries of a Mach-O.
func darwinRPaths(binary string) []string {
	out, err := exec.Command("otool", "-l", binary).Output()
	if err != nil {
		return nil
	}
	var rpaths []string
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if !strings.Contains(line, "cmd LC_RPATH") {
			continue
		}
		for j := i; j < len(lines) && j < i+4; j++ {
			t := strings.TrimSpace(lines[j])
			if strings.HasPrefix(t, "path ") {
				f := strings.Fields(t)
				if len(f) >= 2 {
					rpaths = append(rpaths, f[1])
				}
				break
			}
		}
	}
	return rpaths
}

// resolveDarwin turns an install name into a path on this machine. Mojo emits
// @rpath entries, so the LC_RPATH list decides where a dependency lives.
func resolveDarwin(installName string, rpaths []string, binDir string) (string, error) {
	expand := func(p string) string {
		p = strings.ReplaceAll(p, "@loader_path", binDir)
		p = strings.ReplaceAll(p, "@executable_path", binDir)
		return p
	}
	switch {
	case strings.HasPrefix(installName, "/"):
		return installName, nil
	case strings.HasPrefix(installName, "@rpath/"):
		rest := strings.TrimPrefix(installName, "@rpath/")
		for _, rp := range rpaths {
			candidate := filepath.Join(expand(rp), rest)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("%s: no LC_RPATH resolves it", installName)
	case strings.HasPrefix(installName, "@loader_path"), strings.HasPrefix(installName, "@executable_path"):
		return expand(installName), nil
	}
	return "", fmt.Errorf("%s is neither absolute nor @rpath-based", installName)
}

// linkedLibraries returns library name -> absolute path.
func linkedLibraries(binary string) (map[string]string, error) {
	out := map[string]string{}
	if runtime.GOOS == "darwin" {
		raw, err := exec.Command("otool", "-L", binary).Output()
		if err != nil {
			return out, fmt.Errorf("otool -L: %w", err)
		}
		rpaths := darwinRPaths(binary)
		binDir := filepath.Dir(binary)
		for _, line := range strings.Split(string(raw), "\n")[1:] {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) == 0 {
				continue
			}
			installName := fields[0]
			resolved, err := resolveDarwin(installName, rpaths, binDir)
			if err != nil {
				return out, err
			}
			out[installName] = resolved
		}
		return out, nil
	}

	raw, err := exec.Command("ldd", binary).Output()
	if err != nil {
		return out, fmt.Errorf("ldd: %w", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var path string
		if i := strings.Index(line, "=>"); i >= 0 {
			rest := strings.TrimSpace(line[i+2:])
			if strings.HasPrefix(rest, "not found") || rest == "" {
				name := strings.Fields(line)[0]
				return out, fmt.Errorf("%s is not found on this machine", name)
			}
			path = strings.Fields(rest)[0]
		} else if strings.HasPrefix(line, "/") {
			path = strings.Fields(line)[0]
		}
		if path == "" {
			continue
		}
		out[filepath.Base(path)] = path
	}
	return out, nil
}

// rewriteMacOS points the Mach-O at @loader_path/lib and re-signs it.
func rewriteMacOS(binary string, libs map[string]string) error {
	for installName, src := range libs {
		name := filepath.Base(installName)
		if systemLibs[name] || isSystemPath(src) {
			continue
		}
		args := []string{"-change", installName, "@loader_path/lib/" + name, binary}
		if out, err := exec.Command("install_name_tool", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("install_name_tool %s: %v: %s", name, err, strings.TrimSpace(string(out)))
		}
	}
	// Any Mach-O mutation invalidates the ad-hoc signature on Apple silicon.
	if out, err := exec.Command("codesign", "-s", "-", "--force", binary).CombinedOutput(); err != nil {
		return fmt.Errorf("codesign: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// LinkerArgs flattens the `[linker]` sections of every package.
func LinkerArgs(g *resolve.Graph, projectDir string, extraDirs, extraLibs []string) []string {
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
	for _, d := range extraDirs {
		abs := d
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(projectDir, d)
		}
		add("-L", abs)
		// The linker needs the directory; so does the dynamic loader at build
		// time, before Bundle() copies the library next to the binary.
		args = append(args, "-Xlinker", "-rpath", "-Xlinker", abs)
	}
	for _, l := range extraLibs {
		add("-l", l)
	}
	return args
}

// BinaryPath is where the build writes its result.
func BinaryPath(projectDir, name string) string {
	return filepath.Join(projectDir, ".out", name)
}

// Mojo builds entry into out with the given mojo binary.
func Mojo(mojoBin, projectDir, entry, out, modulesDir, compilerDir string, includes, extra []string) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	args := []string{"build", entry}
	if modulesDir != "" {
		args = append(args, "-I", modulesDir)
	}
	args = append(args, "-I", ".", "-o", out)
	for _, dir := range includes {
		if dir != "" {
			args = append(args, "-I", dir)
		}
	}
	args = append(args, extra...)

	cmd := exec.Command(mojoBin, args...)
	cmd.Dir = projectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"MODULAR_CACHE_DIR="+compilerDir,
	)
	return cmd.Run()
}

// Strip reduces a produced binary in place.
func Strip(path string) error {
	if runtime.GOOS == "darwin" {
		return nil // would invalidate the signature; step 10 re-signs instead
	}
	return stripFile(path)
}

func stripFile(path string) error {
	if _, err := exec.LookPath("strip"); err != nil {
		return nil
	}
	return exec.Command("strip", "-s", path).Run()
}

func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", src)
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, raw, info.Mode()|0o400); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Describe renders a route list for diagnostics.
func Describe(routes []resolve.Route) string {
	var b strings.Builder
	for _, r := range routes {
		fmt.Fprintf(&b, "  %-24s -> %s\n", r.Path, r.Dir)
	}
	return b.String()
}
