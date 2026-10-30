package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/LukasLow/bojo/internal/manifest"
)

// cmdDeps is the dependency front end (D50):
//
//	bojo deps list
//	bojo deps add <url>#<pkg>@<ver> [--as <name>]
//	bojo deps add-python <name>[@<version>]
//	bojo deps check            (not implemented yet: newer versions, CVEs)
func cmdDeps(args []string) int {
	if len(args) == 0 || args[0] == "list" {
		return depsList()
	}
	switch args[0] {
	case "add":
		return cmdGet(args[1:])
	case "add-python":
		return depsAddPython(args[1:])
	case "check":
		fail(errors.New("deps check is not implemented yet (newer versions and CVEs)"))
		return exitUsage
	default:
		fail(fmt.Errorf("unknown deps command %q (list|add|add-python|check)", args[0]))
		return exitUsage
	}
}

func depsList() int {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	cfg, err := manifest.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}

	fmt.Printf("mojo   %s\n", orNone(cfg.Version.Mojo))
	if cfg.Version.Python != "" {
		fmt.Printf("python %s\n", cfg.Version.Python)
	}
	fmt.Println()

	if len(cfg.DepsFrom) == 0 && len(cfg.Deps.Local) == 0 && len(cfg.Deps.Python) == 0 {
		fmt.Println("no dependencies")
		return exitOK
	}
	for repo, pkgs := range cfg.DepsFrom {
		for name, spec := range pkgs {
			ver := spec.Version
			if ver == "" {
				ver = "latest"
			}
			line := fmt.Sprintf("pkg    %s  %s  %s", name, repo, ver)
			if spec.Alias != "" {
				line += "  as " + spec.Alias
			}
			fmt.Println(line)
		}
	}
	for name, d := range cfg.Deps.Local {
		fmt.Printf("local  %s  %s\n", name, d.Path)
	}
	for name, ver := range cfg.Deps.Python {
		fmt.Printf("python %s  %s\n", name, ver)
	}
	return exitOK
}

var pythonName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// depsAddPython records a Python package in [deps.python].
//
// A Python package name can never contain '@', so the single '@' is an
// unambiguous separator. A leading 'v' in the version is dropped because pip
// versions do not carry one.
func depsAddPython(args []string) int {
	if len(args) != 1 {
		fail(errors.New("usage: bojo deps add-python <name>[@<version>]"))
		return exitUsage
	}
	name, version := args[0], "*"
	if at := strings.Index(args[0], "@"); at >= 0 {
		name, version = args[0][:at], args[0][at+1:]
	}
	if !pythonName.MatchString(name) {
		fail(fmt.Errorf("%q is not a valid Python package name", name))
		return exitUsage
	}
	if version == "" {
		version = "*"
	}
	version = strings.TrimPrefix(version, "v")

	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	if _, err := manifest.Load(dir); err != nil {
		fail(err)
		return exitUsage
	}
	path := filepath.Join(dir, "bojo.toml")

	// [deps.python] without [version] python is a broken manifest, so make sure
	// the interpreter is declared first.
	if err := upsertTOMLKey(path, "version", "python", "3.12"); err != nil {
		fail(err)
		return exitUsage
	}
	if err := upsertTOMLKey(path, "deps.python", name, version); err != nil {
		fail(err)
		return exitUsage
	}
	fmt.Printf("added python package %s %s\n", name, version)
	fmt.Println("next: bojo deps list")
	return exitOK
}

// upsertTOMLKey sets `key = "value"` inside [section], creating the section or
// the key when needed. It edits the file textually so comments survive.
func upsertTOMLKey(path, section, key, value string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	header := "[" + section + "]"
	keyLine := fmt.Sprintf("%s = %q", key, value)

	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			start = i
			break
		}
	}
	if start < 0 {
		text := strings.TrimRight(string(raw), "\n")
		text += "\n\n" + header + "\n" + keyLine + "\n"
		return os.WriteFile(path, []byte(text), 0o644)
	}

	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	for i := start + 1; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, key+" ") || strings.HasPrefix(trimmed, key+"=") {
			lines[i] = keyLine
			return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
		}
	}
	insertAt := end
	for insertAt > start+1 && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}
	lines = append(lines[:insertAt], append([]string{keyLine}, lines[insertAt:]...)...)
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func orNone(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}
