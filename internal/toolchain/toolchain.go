// Package toolchain installs and locates Mojo toolchains.
//
// Toolchains are the one exception to the project-local cache rule (D32): they
// are expensive, identical for every project and therefore machine-wide, under
// ~/.bojo/mojo/<version>/ (D43).
//
// Mojo is distributed as a Python package. Installation therefore uses
// `python3 -m venv` plus `pip install mojo==<version>` into that venv. Python is
// needed at install time only; building a pure Mojo program does not use it.
package toolchain

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Root is ~/.bojo/mojo.
func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".bojo", "mojo"), nil
}

// Dir is the install directory for one version.
func Dir(version string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "v"+strings.TrimPrefix(version, "v")), nil
}

// Installed returns the path of the mojo binary for a version, if present.
func Installed(version string) (string, bool) {
	dir, err := Dir(version)
	if err != nil {
		return "", false
	}
	p := filepath.Join(dir, "bin", "mojo")
	if info, err := os.Stat(p); err == nil && info.Mode()&0o111 != 0 {
		return p, true
	}
	return "", false
}

// Resolve returns the mojo binary for the requested version.
//
// An empty version means "whatever is on PATH". A requested version that is not
// installed is an error with the exact next command; never a silent fallback,
// because a different compiler produces incompatible .mojoc files (D8/D35).
func Resolve(version string) (string, error) {
	if version == "" {
		if p, err := exec.LookPath("mojo"); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("no toolchain selected and no mojo on PATH (try `bojo toolchain install 1.2.0`)")
	}
	if p, ok := Installed(version); ok {
		return p, nil
	}
	if p, err := exec.LookPath("mojo"); err == nil {
		if got, ferr := Fingerprint(p); ferr == nil && strings.HasPrefix(got, strings.TrimPrefix(version, "v")) {
			return p, nil
		}
	}
	return "", fmt.Errorf("mojo %s is not installed (run `bojo toolchain install %s`)", version, version)
}

// List returns the installed versions.
func List() ([]string, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		v := strings.TrimPrefix(e.Name(), "v")
		if _, ok := Installed(v); ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// Install creates a venv under ~/.bojo/mojo/<version>/ and installs the official
// Mojo package into it.
func Install(version string) (string, error) {
	version = strings.TrimPrefix(version, "v")
	if p, ok := Installed(version); ok {
		return p, nil
	}
	// `bojo toolchain mojo v1.2` should find an installed 1.2.x.
	if versions, err := List(); err == nil {
		if resolved, ok := matchInstalled(version, versions); ok {
			if p, ok := Installed(resolved); ok {
				return p, nil
			}
		}
	}

	python, err := exec.LookPath("python3")
	if err != nil {
		return "", fmt.Errorf("python3 is required to install a toolchain (Mojo is distributed as a Python package)")
	}
	if runtime.GOOS == "linux" && isMusl() {
		return "", fmt.Errorf("Mojo ships glibc wheels only; this system uses musl. Use a glibc-based image or host")
	}

	dest, err := Dir(version)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	// A venv bakes absolute paths into its scripts, so it must be created at the
	// final location; a failed install removes the directory again.
	tmp := dest
	_ = os.RemoveAll(tmp)

	if out, err := run("", python, "-m", "venv", tmp); err != nil {
		return "", fmt.Errorf("creating a venv: %v: %s", err, out)
	}
	pip := filepath.Join(tmp, "bin", "pip")
	if out, err := run("", pip, "install", "--quiet", "--no-cache-dir", "mojo=="+version); err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("installing mojo %s: %v: %s", version, err, lastLines(out, 8))
	}

	p, ok := Installed(version)
	if !ok {
		return "", fmt.Errorf("installed mojo %s but %s is missing", version, filepath.Join(dest, "bin", "mojo"))
	}
	return p, nil
}

// Fingerprint runs `<mojo> --version` and returns e.g. 1.2.0-2016d0cd.
func Fingerprint(binary string) (string, error) {
	out, err := run("", binary, "--version")
	if err != nil {
		return "", err
	}
	return ParseFingerprint(out), nil
}

// ParseFingerprint turns `Mojo 1.2.0 (2016d0cd)` into `1.2.0-2016d0cd` (D35).
func ParseFingerprint(versionOutput string) string {
	line := strings.TrimSpace(versionOutput)
	ver, build := "", ""
	for _, f := range strings.Fields(line) {
		t := strings.TrimSpace(f)
		if strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")") {
			build = strings.Trim(t, "()")
			continue
		}
		if t != "" && t[0] >= '0' && t[0] <= '9' && ver == "" {
			ver = t
		}
	}
	switch {
	case ver != "" && build != "":
		return ver + "-" + build
	case ver != "":
		return ver
	default:
		return line
	}
}

// Remove deletes an installed Mojo toolchain. A version prefix is accepted when
// it matches exactly one installed version.
func Remove(version string) error {
	version = strings.TrimPrefix(version, "v")
	if _, ok := Installed(version); !ok {
		versions, err := List()
		if err != nil {
			return err
		}
		resolved, ok := matchInstalled(version, versions)
		if !ok {
			return fmt.Errorf("mojo %s is not installed", version)
		}
		version = resolved
	}
	dir, err := Dir(version)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// matchInstalled resolves a version prefix ("1.2") to an installed version
// ("1.2.0"). An ambiguous prefix is not matched.
func matchInstalled(want string, installed []string) (string, bool) {
	var hits []string
	for _, v := range installed {
		if v == want || strings.HasPrefix(v, want+".") {
			hits = append(hits, v)
		}
	}
	if len(hits) == 1 {
		return hits[0], true
	}
	return "", false
}

// isMusl reports whether the C library is musl (Alpine and friends).
func isMusl() bool {
	if _, err := os.Stat("/lib/ld-musl-x86_64.so.1"); err == nil {
		return true
	}
	if _, err := os.Stat("/lib/ld-musl-aarch64.so.1"); err == nil {
		return true
	}
	return false
}

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
