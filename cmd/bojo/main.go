// Command bojo is a package manager and build driver for Mojo.
//
// This file is the thin CLI shell: it parses arguments, prints messages in the
// style the specification prescribes (one fact per line, complete sentences,
// distinct exit codes) and delegates to internal packages.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/LukasLow/bojo/internal/manifest"
	"github.com/LukasLow/bojo/skill"
)

const version = "0.1.0-dev"

// Exit codes are part of the contract; agents branch on them.
const (
	exitOK       = 0
	exitUsage    = 1
	exitBlocked  = 2 // hook approval missing; nothing was executed
	exitBuild    = 3
	exitNetwork  = 4
	exitLockFile = 5
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Printf("bojo %s\n", version)
		return exitOK
	case "skill":
		runtime := ""
		if len(args) > 1 {
			runtime = args[1]
		}
		text, err := skill.Text(runtime)
		if err != nil {
			fail(err)
			return exitUsage
		}
		fmt.Print(text)
		return exitOK
	case "doctor":
		return cmdDoctor()
	case "init", "new":
		return cmdInit(args[1:])
	case "build":
		return cmdBuild(args[1:])
	case "clean":
		return cmdClean(args[1:])
	case "cache":
		return cmdCache(args[1:])
	case "help", "--help", "-h":
		usage()
		return exitOK
	default:
		fail(fmt.Errorf("unknown command %q", args[0]))
		usage()
		return exitUsage
	}
}

func usage() {
	fmt.Print(`bojo — package manager and build driver for Mojo

Usage:
  bojo init                     create a project
  bojo build [--locked]         build into out/
  bojo run <url>#<pkg>@<ver>    build and run a remote command (not yet)
  bojo install <url>#<pkg>@<ver> install a remote command (not yet)
  bojo get <url>#<pkg>@<ver>    add a dependency (not yet)
  bojo tree | why               dependency graph (not yet)
  bojo allow <id> | --list      approve or list hooks (not yet)
  bojo verify                   verify the lock (not yet)
  bojo doctor                   check toolchain, compiler and PATH
  bojo cache list | clear       inspect or clear .bojo/cache
  bojo clean [--all]            remove .bojo/ (--all: also out/)
  bojo skill [claude|codex|opencode]
                                print the agent skill as markdown
  bojo version                  print the version
`)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "bojo: %v\n", err)
}

// projectRoot returns the working directory, which is where bojo.toml lives.
func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return dir, nil
}

func cmdInit(args []string) int {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	if _, err := os.Stat(filepath.Join(dir, "bojo.toml")); err == nil {
		fail(fmt.Errorf("bojo.toml already exists in %s", dir))
		return exitUsage
	}

	name := filepath.Base(dir)
	if len(args) > 0 {
		name = args[0]
	}

	manifest := fmt.Sprintf(`[project]
name    = %q
version = "0.1.0"
entry   = "main.mojo"

[toolchain]
mojo   = "%s"
target = "%s"

[build]
output = "dir"
strip  = true
`, name, mojoVersionOr("1.2.0"), hostTarget())

	mainMojo := `def main():
    print("hello from bojo")
`

	if err := os.WriteFile(filepath.Join(dir, "bojo.toml"), []byte(manifest), 0o644); err != nil {
		fail(err)
		return exitUsage
	}
	if _, err := os.Stat(filepath.Join(dir, "main.mojo")); os.IsNotExist(err) {
		if err := os.WriteFile(filepath.Join(dir, "main.mojo"), []byte(mainMojo), 0o644); err != nil {
			fail(err)
			return exitUsage
		}
	}
	ensureGitignore(dir, ".bojo/")
	ensureGitignore(dir, "out/")

	fmt.Printf("created bojo.toml in %s\n", dir)
	fmt.Println("next: bojo build")
	return exitOK
}

func cmdBuild(args []string) int {
	locked := false
	for _, a := range args {
		switch a {
		case "--locked":
			locked = true
		default:
			fail(fmt.Errorf("unknown flag %q", a))
			return exitUsage
		}
	}

	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	m, err := manifest.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}

	if locked {
		if _, err := os.Stat(filepath.Join(dir, "bojo.lock")); err != nil {
			fail(errors.New("--locked given but bojo.lock does not exist (run `bojo update`)"))
			return exitLockFile
		}
	}
	if len(m.Deps) > 0 || len(m.DepsFrom) > 0 {
		fail(errors.New("dependency resolution is not implemented in this build; remove deps or wait for `bojo get`"))
		return exitUsage
	}

	modules := filepath.Join(dir, ".bojo", "modules")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		fail(err)
		return exitUsage
	}
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fail(err)
		return exitUsage
	}

	binary := m.Project.Name
	if m.Build.Output == "onefile" {
		fmt.Println("note: output = \"onefile\" is not implemented yet; writing a directory build")
	}
	out := filepath.Join(outDir, binary)

	mojoArgs := []string{
		"build", m.Project.Entry,
		"-I", modules,
		"-I", ".",
		"-o", out,
	}
	fmt.Printf("building %s\n", m.Project.Entry)
	cmd := exec.Command("mojo", mojoArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "MODULAR_CACHE_DIR="+filepath.Join(dir, ".bojo", "mojo-cache"))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			fail(fmt.Errorf("mojo build failed (exit %d); run `bojo doctor`", ee.ExitCode()))
			return exitBuild
		}
		fail(err)
		return exitBuild
	}

	fmt.Printf("ok: %s\n", out)
	return exitOK
}

func cmdDoctor() int {
	fmt.Printf("bojo %s\n", version)
	fmt.Println()

	problems := 0
	problems += check("mojo", "version")
	problems += check("cc", "--version")
	problems += check("git", "--version")

	if path, err := exec.LookPath("mojo"); err == nil {
		fmt.Printf("  mojo path   %s\n", path)
	}

	binDir := filepath.Join(os.Getenv("HOME"), ".bojo", "bin")
	if !pathContains(binDir) {
		fmt.Printf("  PATH        %s is not in PATH\n", binDir)
		fmt.Printf("              export PATH=\"%s:$PATH\"\n", binDir)
	}

	fmt.Println()
	if problems > 0 {
		fmt.Printf("%d problem(s) found.\n", problems)
		return exitUsage
	}
	fmt.Println("all checks passed.")
	return exitOK
}

// check runs `<name> <arg>` and prints a single line with the first output line.
func check(name, arg string) int {
	path, err := exec.LookPath(name)
	if err != nil {
		fmt.Printf("  %-11s missing — required to build\n", name)
		return 1
	}
	out, err := exec.Command(path, arg).Output()
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if err != nil || line == "" {
		fmt.Printf("  %-11s found at %s (version unknown)\n", name, path)
		return 0
	}
	fmt.Printf("  %-11s %s\n", name, line)
	return 0
}

func pathContains(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

func cmdClean(args []string) int {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	all := false
	for _, a := range args {
		if a == "--all" {
			all = true
			continue
		}
		fail(fmt.Errorf("unknown flag %q", a))
		return exitUsage
	}

	removed := 0
	for _, p := range []string{filepath.Join(dir, ".bojo")} {
		if ok, err := removeIfExists(p); err != nil {
			fail(err)
			return exitUsage
		} else if ok {
			fmt.Printf("removed %s\n", p)
			removed++
		}
	}
	if all {
		if ok, err := removeIfExists(filepath.Join(dir, "out")); err != nil {
			fail(err)
			return exitUsage
		} else if ok {
			fmt.Printf("removed %s\n", filepath.Join(dir, "out"))
			removed++
		}
	}
	if removed == 0 {
		fmt.Println("nothing to clean")
	}
	return exitOK
}

func cmdCache(args []string) int {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	cacheDir := filepath.Join(dir, ".bojo", "cache")

	if len(args) == 0 {
		fail(errors.New("usage: bojo cache list|clear"))
		return exitUsage
	}
	switch args[0] {
	case "list":
		src := filepath.Join(cacheDir, "src")
		entries, err := os.ReadDir(src)
		if err != nil {
			fmt.Println("cache is empty")
			return exitOK
		}
		for _, e := range entries {
			fmt.Printf("  %s\n", e.Name())
		}
		fmt.Printf("%d repo(s) cached in %s\n", len(entries), cacheDir)
		return exitOK
	case "clear":
		ok, err := removeIfExists(cacheDir)
		if err != nil {
			fail(err)
			return exitUsage
		}
		if ok {
			fmt.Println("cache cleared")
		} else {
			fmt.Println("cache is already empty")
		}
		return exitOK
	default:
		fail(fmt.Errorf("unknown cache command %q (list|clear)", args[0]))
		return exitUsage
	}
}

func removeIfExists(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	return true, os.RemoveAll(path)
}

// ensureGitignore appends entry to .gitignore unless it is already present.
func ensureGitignore(dir, entry string) {
	path := filepath.Join(dir, ".gitignore")
	raw, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == entry {
			return
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(raw) > 0 && !strings.HasSuffix(string(raw), "\n") {
		fmt.Fprintln(f)
	}
	fmt.Fprintln(f, entry)
}

// mojoVersionOr returns the installed Mojo version, or fallback.
func mojoVersionOr(fallback string) string {
	out, err := exec.Command("mojo", "--version").Output()
	if err != nil {
		return fallback
	}
	line := strings.TrimSpace(string(out))
	fields := strings.Fields(line) // "Mojo 1.2.0 (2016d0cd)"
	if len(fields) >= 2 {
		return fields[1]
	}
	return fallback
}

// hostTarget maps GOOS/GOARCH to the target triple Mojo uses.
func hostTarget() string {
	arch := "aarch64"
	if runtime.GOARCH == "amd64" {
		arch = "x86_64"
	}
	switch runtime.GOOS {
	case "darwin":
		return arch + "-apple-darwin"
	default:
		return arch + "-unknown-linux-gnu"
	}
}
