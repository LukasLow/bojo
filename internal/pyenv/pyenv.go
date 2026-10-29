// Package pyenv provisions the bundled Python environment for a project:
// a relocatable interpreter (from the toolchain store) plus wheels, installed
// without ever executing an sdist (DESIGN §20, D44/D48).
package pyenv

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LukasLow/bojo/internal/toolchain"
)

// Result describes a provisioned environment.
type Result struct {
	Interpreter  string            // absolute path to the interpreter
	PythonHome   string            // prefix that PYTHONHOME must point at
	SitePackages string            // directory that PYTHONPATH must point at
	Hashes       map[string]string // package -> sha256 of the wheel that was used
}

// Supported reports whether the version is inside Mojo's Python window.
func Supported(version string) bool {
	switch strings.TrimPrefix(version, "v") {
	case "3.10", "3.11", "3.12", "3.13", "3.14":
		return true
	default:
		return false
	}
}

// Ensure downloads wheels, verifies they are wheels, installs them into a
// project-local site-packages and scans for executable .pth files.
func Ensure(projectDir, version string, packages map[string]string) (*Result, error) {
	if !Supported(version) {
		return nil, fmt.Errorf("python %s is outside the supported range 3.10–3.14", version)
	}
	interpreter, ok := toolchain.PythonInstalled(version)
	if !ok {
		return nil, fmt.Errorf("python %s is not installed (run `bojo toolchain install python %s`)", version, version)
	}

	base := filepath.Join(projectDir, ".bojo", "python")
	wheels := filepath.Join(base, "wheels")
	site := filepath.Join(base, "site-packages")
	if err := os.MkdirAll(wheels, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(site, 0o755); err != nil {
		return nil, err
	}

	specs := make([]string, 0, len(packages))
	for name, ver := range packages {
		if ver == "" || ver == "*" {
			specs = append(specs, name)
			continue
		}
		specs = append(specs, name+"=="+ver)
	}
	sort.Strings(specs)

	// 1. Download wheels only. An sdist is refused here, before anything runs.
	args := append([]string{"-m", "pip", "download", "--only-binary=:all:", "--dest", wheels}, specs...)
	if out, err := run(interpreter, args...); err != nil {
		return nil, fmt.Errorf("downloading wheels (an sdist-only package needs an explicit approval): %v: %s", err, tail(out))
	}

	// 2. Hash what we got, so the lock can pin it (D48).
	hashes := map[string]string{}
	entries, err := os.ReadDir(wheels)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".whl") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(wheels, e.Name()))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		// "numpy-2.1.0-cp312-…whl" -> "numpy"
		name := e.Name()
		if i := strings.Index(name, "-"); i > 0 {
			name = name[:i]
		}
		hashes[name] = hex.EncodeToString(sum[:])
	}

	// 3. Install from the local wheels, no index, no build isolation.
	installArgs := append([]string{
		"-m", "pip", "install", "--no-index", "--find-links", wheels,
		"--only-binary=:all:", "--target", site, "--upgrade", "--quiet",
	}, specs...)
	if out, err := run(interpreter, installArgs...); err != nil {
		return nil, fmt.Errorf("installing into the bundled environment: %v: %s", err, tail(out))
	}

	// 4. A .pth file runs code at every interpreter start. Refuse it (D48).
	if err := scanPth(site); err != nil {
		return nil, err
	}

	return &Result{
		Interpreter:  interpreter,
		PythonHome:   filepath.Dir(filepath.Dir(interpreter)),
		SitePackages: site,
		Hashes:       hashes,
	}, nil
}

// scanPth rejects .pth files that are not a plain list of paths.
func scanPth(site string) error {
	return filepath.WalkDir(site, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".pth") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		line := 0
		for sc.Scan() {
			line++
			text := strings.TrimSpace(sc.Text())
			if text == "" || strings.HasPrefix(text, "#") {
				continue
			}
			if strings.HasPrefix(text, "import ") || strings.HasPrefix(text, "exec(") {
				return fmt.Errorf("%s:%d executes code at interpreter start: %q", path, line, text)
			}
		}
		return sc.Err()
	})
}

// CopyTree copies the interpreter and site-packages into outDir/python.
// Data-only directories (share, doc, man) are skipped: they hold terminfo and
// documentation, cost tens of megabytes and contain self-referential links.
func CopyTree(r *Result, outDir string) error {
	dst := filepath.Join(outDir, "python")
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	entries, err := os.ReadDir(r.PythonHome)
	if err != nil {
		return err
	}
	for _, e := range entries {
		switch e.Name() {
		case "share", "doc", "man":
			continue
		}
		if err := copyDir(filepath.Join(r.PythonHome, e.Name()),
			filepath.Join(dst, "runtime", e.Name())); err != nil {
			return err
		}
	}
	if err := copyDir(r.SitePackages, filepath.Join(dst, "site-packages")); err != nil {
		return err
	}
	return stripInterpreter(filepath.Join(dst, "runtime"))
}

// stripInterpreter removes debug information from the interpreter only.
// Wheel contents are left untouched: stripping a vendor binary can break its
// program headers ("ELF load command address/offset not page-aligned").
func stripInterpreter(runtime string) error {
	return filepath.WalkDir(runtime, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "python3") || strings.HasPrefix(name, "libpython") {
			_ = exec.Command("strip", "-x", path).Run()
		}
		return nil
	})
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode()|0o400)
	})
}

func run(bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, " | ")
}
