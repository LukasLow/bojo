// Command bojo is a package manager and build driver for Mojo.
//
// The CLI shell stays thin: it parses arguments, prints messages in the shape
// the specification prescribes (one fact per line, complete sentences, distinct
// exit codes) and delegates to the internal packages.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/LukasLow/bojo/internal/build"
	"github.com/LukasLow/bojo/internal/cache"
	"github.com/LukasLow/bojo/internal/hooks"
	"github.com/LukasLow/bojo/internal/lock"
	"github.com/LukasLow/bojo/internal/manifest"
	"github.com/LukasLow/bojo/internal/resolve"
	"github.com/LukasLow/bojo/internal/toolchain"
	"github.com/LukasLow/bojo/internal/vendor"
	"github.com/LukasLow/bojo/skill"
)

const version = "0.1.0-dev"

const (
	exitOK       = 0
	exitUsage    = 1
	exitBlocked  = 2
	exitBuild    = 3
	exitNetwork  = 4
	exitLockFile = 5
)

func main() { os.Exit(run(os.Args[1:])) }

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
		runtimeName := ""
		if len(args) > 1 {
			runtimeName = args[1]
		}
		text, err := skill.Text(runtimeName)
		if err != nil {
			fail(err)
			return exitUsage
		}
		fmt.Print(text)
		return exitOK
	case "doctor":
		return cmdDoctor()
	case "toolchain":
		return cmdToolchains(args[1:])
	case "init", "new":
		return cmdInit(args[1:])
	case "get":
		return cmdGet(args[1:])
	case "deps":
		return cmdDeps(args[1:])
	case "update":
		return cmdUpdate(args[1:])
	case "build":
		return cmdBuild(args[1:])
	case "tree":
		return cmdTree()
	case "why":
		return cmdWhy(args[1:])
	case "allow":
		return cmdAllow(args[1:])
	case "verify":
		return cmdVerify()
	case "run":
		return cmdRun(args[1:], false)
	case "install":
		return cmdRun(args[1:], true)
	case "cache":
		return cmdCache(args[1:])
	case "repo-index":
		return cmdRepoIndex(args[1:])
	case "search":
		return cmdSearch(args[1:])
	case "channel":
		return cmdChannel(args[1:])
	case "validate":
		return cmdValidate(args[1:])
	case "test":
		return cmdTest(args[1:])
	case "vendor":
		return cmdVendor(args[1:])
	case "clean":
		return cmdClean(args[1:])
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
  bojo init                       create a project
  bojo deps list                  show dependencies
  bojo deps add <url>#<pkg>@<ver> add a repository dependency
  bojo deps add-python <name>[@<ver>]
                                  add a Python package
  bojo update                     resolve and write bojo.lock
  bojo build [--locked]           build into out/
  bojo run <url>#<pkg>@<ver>      ephemeral build and run a remote command
  bojo install <url>#<pkg>@<ver>  ephemeral build, binary to ~/.bojo/bin
  bojo tree                       print the resolved graph
  bojo why <pkg>                  explain where a package comes from
  bojo allow <id>|--list          approve or list hooks
  bojo verify                     verify the lock against fresh checkouts
  bojo doctor                     check toolchain, compiler and PATH
  bojo toolchain install <ver>    install a Mojo toolchain into ~/.bojo/mojo
  bojo toolchain list             list installed toolchains
  bojo cache list|clear           inspect or clear .bojo/cache
  bojo clean [--all]              remove .bojo/ (--all: also out/)
  bojo skill [claude|codex|opencode]
                                  print the agent skill as markdown
  bojo version                    print the version
`)
}

func fail(err error) { fmt.Fprintf(os.Stderr, "bojo: %v\n", err) }

// cmdToolchains handles the namespaced toolchain commands (D49):
//
//	bojo toolchain                     list everything
//	bojo toolchain list                list everything
//	bojo toolchain mojo list           list one family
//	bojo toolchain install python 3.12 install one family
//	bojo toolchain mojo v1.2           shorthand for install
func cmdToolchains(args []string) int {
	family := ""
	rest := args
	if len(rest) > 0 && isFamily(rest[0]) {
		family = rest[0]
		rest = rest[1:]
	}
	if len(rest) == 0 || rest[0] == "list" {
		return toolchainList(family)
	}
	action := "install"
	if rest[0] == "install" || rest[0] == "remove" {
		action = rest[0]
		rest = rest[1:]
		if family == "" {
			if len(rest) == 0 || !isFamily(rest[0]) {
				fail(fmt.Errorf("usage: bojo toolchain %s <mojo|python> <version>", action))
				return exitUsage
			}
			family = rest[0]
			rest = rest[1:]
		}
	}
	if family == "" {
		fail(errors.New("usage: bojo toolchain [list] | install|remove <mojo|python> <version>"))
		return exitUsage
	}
	if len(rest) != 1 {
		fail(fmt.Errorf("usage: bojo toolchain %s %s <version>", action, family))
		return exitUsage
	}
	if action == "remove" {
		return toolchainRemove(family, rest[0])
	}
	return toolchainInstall(family, rest[0])
}

func isFamily(s string) bool { return s == "mojo" || s == "python" }

func toolchainList(family string) int {
	if family == "" || family == "mojo" {
		versions, err := toolchain.List()
		if err != nil {
			fail(err)
			return exitUsage
		}
		printVersions("mojo", versions)
	}
	if family == "" || family == "python" {
		versions, err := toolchain.PythonList()
		if err != nil {
			fail(err)
			return exitUsage
		}
		printVersions("python", versions)
	}
	return exitOK
}

func toolchainRemove(family, version string) int {
	var err error
	switch family {
	case "mojo":
		err = toolchain.Remove(version)
	case "python":
		err = toolchain.PythonRemove(version)
	default:
		fail(fmt.Errorf("unknown toolchain family %q (mojo|python)", family))
		return exitUsage
	}
	if err != nil {
		fail(err)
		return exitUsage
	}
	fmt.Printf("removed %s %s\n", family, strings.TrimPrefix(version, "v"))
	return exitOK
}

func toolchainInstall(family, version string) int {
	var (
		binary string
		err    error
	)
	switch family {
	case "mojo":
		binary, err = toolchain.Install(version)
	case "python":
		binary, err = toolchain.PythonInstall(version)
	default:
		fail(fmt.Errorf("unknown toolchain family %q (mojo|python)", family))
		return exitUsage
	}
	if err != nil {
		fail(err)
		return exitNetwork
	}
	fmt.Printf("installed %s %s\n  %s\n", family, strings.TrimPrefix(version, "v"), binary)
	return exitOK
}

func printVersions(family string, versions []string) {
	if len(versions) == 0 {
		fmt.Printf("%s: none installed\n", family)
		return
	}
	for _, v := range versions {
		fmt.Printf("%s: %s\n", family, v)
	}
}

// resolveExit maps a resolution failure: remote problems are exit 4, anything
// else (bad manifest, unknown package, cycle) is a configuration error, exit 1.
func resolveExit(err error) int {
	var fe *cache.FetchError
	if errors.As(err, &fe) {
		return exitNetwork
	}
	return exitUsage
}

func projectRoot() (string, error) { return os.Getwd() }

// ---------------------------------------------------------------------------
// init / doctor / clean / cache

func cmdInit(args []string) int {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	if _, err := os.Stat(filepath.Join(dir, "bojo.toml")); err == nil {
		fail(errors.New("bojo.toml already exists in this directory"))
		return exitUsage
	}
	name := filepath.Base(dir)
	if len(args) > 0 {
		name = args[0]
	}

	cfg := fmt.Sprintf(`[project]
name    = %q
version = "0.1.0"
entry   = "main.mojo"

[version]
mojo = %q

[build]
output = "dir"
strip  = true
`, name, mojoVersionOr("1.2.0"))

	mainMojo := "def main():\n    print(\"hello from bojo\")\n"

	if err := os.WriteFile(filepath.Join(dir, "bojo.toml"), []byte(cfg), 0o644); err != nil {
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

func cmdDoctor() int {
	fmt.Printf("bojo %s\n\n", version)
	problems := 0
	problems += check("mojo", "--version")
	problems += check("cc", "--version")
	problems += check("git", "--version")

	if out, err := exec.Command("mojo", "--version").Output(); err == nil {
		fmt.Printf("  %-11s fingerprint %s\n", "toolchain", fingerprint(string(out)))
	}
	binDir := filepath.Join(os.Getenv("HOME"), ".bojo", "bin")
	if !pathContains(binDir) {
		fmt.Printf("  %-11s not in PATH\n", "PATH")
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

func cmdClean(args []string) int {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	all := false
	for _, a := range args {
		switch a {
		case "--all":
			all = true
		default:
			fail(fmt.Errorf("unknown flag %q", a))
			return exitUsage
		}
	}
	removed := 0
	if ok, err := removeIfExists(filepath.Join(dir, ".bojo")); err != nil {
		fail(err)
		return exitUsage
	} else if ok {
		fmt.Println("removed .bojo/")
		removed++
	}
	if all {
		if ok, err := removeIfExists(filepath.Join(dir, ".out")); err != nil {
			fail(err)
			return exitUsage
		} else if ok {
			fmt.Println("removed out/")
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
		entries, err := os.ReadDir(filepath.Join(cacheDir, "src"))
		if err != nil {
			fmt.Println("cache is empty")
			return exitOK
		}
		for _, e := range entries {
			fmt.Printf("  %s\n", e.Name())
		}
		fmt.Printf("%d repository/ies cached\n", len(entries))
		return exitOK
	case "clear":
		if ok, err := removeIfExists(cacheDir); err != nil {
			fail(err)
			return exitUsage
		} else if ok {
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

// ---------------------------------------------------------------------------
// get / update

func cmdGet(args []string) int {
	if len(args) == 0 {
		fail(errors.New("usage: bojo get <url>#<pkg>@<version> [--as <name>]"))
		return exitUsage
	}
	spec, err := parseSpec(args[0])
	if err != nil {
		fail(err)
		return exitUsage
	}
	aliasName := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--as" && i+1 < len(args) {
			aliasName = args[i+1]
			i++
			continue
		}
		if args[i] == "--lib" && i+1 < len(args) {
			for _, l := range strings.Split(args[i+1], ",") {
				if l = strings.TrimSpace(l); l != "" {
					spec.pkgs = append(spec.pkgs, l)
				}
			}
			i++
			continue
		}
		fail(fmt.Errorf("unknown argument %q", args[i]))
		return exitUsage
	}

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
	for _, d := range cfg.DepsFrom {
		if d.Repo == spec.repo && d.Tag == spec.tag() {
			for _, l := range d.Libs {
				if l == spec.pkg {
					fmt.Printf("already declared: %s@%s\n", spec.pkg, spec.version)
					return exitOK
				}
			}
		}
	}

	if spec.pkg != "" && len(spec.pkgs) == 0 {
		spec.pkgs = []string{spec.pkg}
	}
	if len(spec.pkgs) == 0 {
		fail(errors.New("which package? use <url>@<pkg>/<version> or --lib <pkg>"))
		return exitUsage
	}
	if err := appendDepFrom(dir, spec, aliasName); err != nil {
		fail(err)
		return exitUsage
	}
	fmt.Printf("added %s@%s from %s\n", spec.pkg, spec.version, spec.repo)
	fmt.Println("next: bojo update")
	return exitOK
}

func cmdUpdate(args []string) int {
	if len(args) > 0 {
		fail(fmt.Errorf("unknown argument %q", args[0]))
		return exitUsage
	}
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
	lf, err := lock.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}

	store := cache.New(dir)
	r := &resolve.Resolver{Store: store, Locked: lf.TreesByCommit()}
	g, err := r.Resolve(cfg)
	if err != nil {
		fail(err)
		return resolveExit(err)
	}
	if err := writeLock(dir, toolchainFingerprint(), g); err != nil {
		fail(err)
		return exitUsage
	}
	fmt.Printf("resolved %d package(s)\n", len(g.Packages))
	if len(g.Hooks) > 0 {
		fmt.Printf("%d hook(s) will need approval before they run\n", len(g.Hooks))
	}
	return exitOK
}

// ---------------------------------------------------------------------------
// build

// cmdVendor materialises .vendor from the graph, or checks it (--check).
func cmdVendor(args []string) int {
	check := false
	for _, a := range args {
		switch a {
		case "--check":
			check = true
		default:
			fail(fmt.Errorf("unknown flag %q", a))
			return exitUsage
		}
	}
	dir, g, code := resolveProject()
	if code != exitOK {
		return code
	}
	if check {
		if err := vendor.Compare(dir, g); err != nil {
			fail(err)
			return exitLockFile
		}
		fmt.Println("ok: .vendor matches the graph")
		return exitOK
	}
	m, err := vendor.Build(dir, g)
	if err != nil {
		fail(err)
		return exitBuild
	}
	fmt.Printf("ok: %d package(s) in .vendor\n", len(m.Entries))
	return exitOK
}

// packageByShortName finds a package by its declared name, since the graph is
// keyed by module path (D65).
func packageByShortName(g *resolve.Graph, name string) (*resolve.Package, bool) {
	for _, p := range g.Packages {
		if p.Name == name {
			return p, true
		}
	}
	return nil, false
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
	cfg, err := manifest.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}
	lf, err := lock.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}
	if locked && len(lf.Packages) == 0 && (len(cfg.DepsFrom) > 0 || len(cfg.Deps.Local) > 0) {
		fail(errors.New("--locked given but bojo.lock has no packages (run `bojo update`)"))
		return exitLockFile
	}

	store := cache.New(dir)
	r := &resolve.Resolver{Store: store, Locked: lf.TreesByCommit()}
	g, err := r.Resolve(cfg)
	if err != nil {
		fail(err)
		return resolveExit(err)
	}

	if locked {
		if err := compareLock(lf, g); err != nil {
			fail(err)
			return exitLockFile
		}
	}
	mojoBin, err := toolchain.Resolve(cfg.Version.Mojo)
	if err != nil {
		fail(err)
		return exitUsage
	}
	if cfg.Build.Output == "onefile" {
		return buildOnefile(dir, cfg, g, mojoBin)
	}

	allow, err := hooks.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}
	lockedCommits := g.LockedCommits()
	for _, e := range allow.Stale(lockedCommits) {
		fmt.Printf("note: ignoring stale approval %s for %s@%s (not in the lock)\n", e.ID(), e.Pkg, shortSHA(e.Commit))
	}
	if code := gate(dir, allow.Filter(lockedCommits), g); code != exitOK {
		return code
	}

	release, err := build.Lock(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}
	defer release()

	if _, err := vendor.Build(dir, g); err != nil {
		fail(err)
		return exitUsage
	}
	modulesDir := ""
	includes := append(vendor.Roots(dir), cfg.Build.Include...)
	if err := runHooks(cfg, g, "pre-build", dir); err != nil {
		fail(err)
		return exitBuild
	}
	out := build.BinaryPath(dir, cfg.Project.Name)
	linkerArgs := append(build.RuntimeRPATH(), build.LinkerArgs(g, dir, cfg.Linker.LibDirs, cfg.Linker.Libs)...)
	if err := build.Mojo(mojoBin, dir, cfg.Project.Entry, out, modulesDir, cache.New(dir).Compiler(), includes, linkerArgs); err != nil {
		fail(fmt.Errorf("mojo build failed: %w", err))
		return exitBuild
	}
	if cfg.Build.Strip {
		_ = build.Strip(out)
	}
	bundled, err := build.Bundle(out)
	if err != nil {
		fail(fmt.Errorf("bundling runtime libraries: %w", err))
		return exitBuild
	}
	if len(bundled) > 0 {
		fmt.Printf("bundled %d library/ies next to the binary\n", len(bundled))
	}
	if err := bundleRuntimeFiles(dir, g, filepath.Dir(out)); err != nil {
		fail(err)
		return exitBuild
	}
	if cfg.Version.Python != "" {
		if code := bundlePython(dir, cfg, out); code != exitOK {
			return code
		}
	}
	if err := runHooks(cfg, g, "post-build", dir); err != nil {
		fail(err)
		return exitBuild
	}
	fmt.Printf("ok: %s\n", out)
	return exitOK
}

// compareLock enforces --locked (DESIGN §11 step 3, exit 5).
func compareLock(lf *lock.File, g *resolve.Graph) error {
	want := map[string]lock.Entry{}
	for _, e := range lf.Packages {
		want[e.Name] = e
	}
	for _, e := range g.Lock {
		w, ok := want[e.Name]
		if !ok {
			return fmt.Errorf("lock is missing %q (run `bojo update`)", e.Name)
		}
		if w.Commit != e.Commit {
			return fmt.Errorf("%q: lock pins %s but the manifest resolves %s (run `bojo update`)",
				e.Name, shortSHA(w.Commit), shortSHA(e.Commit))
		}
	}
	if len(want) != len(g.Lock) {
		return errors.New("lock contains packages the manifest no longer uses (run `bojo update`)")
	}
	return nil
}

// runHooks executes one phase. Foreign hooks run in the package directory (they
// may write there, D33); the project's own hooks run in the project root.
func runHooks(cfg *manifest.File, g *resolve.Graph, phase, projectDir string) error {
	for _, r := range g.Hooks {
		if r.Hook != phase {
			continue
		}
		p := g.Packages[r.Pkg]
		if p == nil {
			continue
		}
		fmt.Printf("hook %s: %s (%s)\n", phase, strings.Join(r.Command, " "), r.Pkg)
		if err := hooks.Run(r.Command, p.Dir); err != nil {
			return fmt.Errorf("hook %q of %s failed: %w", phase, r.Pkg, err)
		}
	}
	for _, c := range cfg.Hooks[phase] {
		if len(c) == 0 {
			continue
		}
		fmt.Printf("own hook %s: %s\n", phase, strings.Join(c, " "))
		if err := hooks.Run(c, projectDir); err != nil {
			return fmt.Errorf("own hook %q failed: %w", phase, err)
		}
	}
	return nil
}

// gate prints every unapproved hook and returns exit 2 when something is
// blocked. It executes nothing (DESIGN §12.2).
func gate(projectDir string, allow *hooks.Store, g *resolve.Graph) int {
	blocked := hooks.Blocked(allow, g.Hooks)
	if len(blocked) == 0 {
		_ = os.Remove(filepath.Join(projectDir, ".bojo", "pending.json"))
		return exitOK
	}

	var entries []hooks.Entry
	for _, r := range blocked {
		e := r.Entry()
		entries = append(entries, e)
		fmt.Printf("blocked: pkg %q wants to run a hook\n", r.Pkg)
		fmt.Printf("  repo     %s\n", r.Repo)
		fmt.Printf("  commit   %s\n", shortSHA(r.Commit))
		fmt.Printf("  pkg      %s\n", r.Pkg)
		fmt.Printf("  hook     %s\n", r.Hook)
		fmt.Printf("  command  %s\n", strings.Join(r.Command, " "))
		if e.Binary != "" {
			fmt.Printf("  binary   %s\n", e.Binary)
		}
		fmt.Println()
		fmt.Printf("Not run. Inspect the repo and the command above, then approve with:\n\n  bojo allow %s\n\n", e.ID())
	}
	_ = writePending(projectDir, entries)
	fmt.Printf("Blocked by %d hook(s). Nothing was executed.\n", len(blocked))
	return exitBlocked
}

// ---------------------------------------------------------------------------
// tree / why / verify / allow

func cmdTree() int {
	dir, g, code := resolveProject()
	if code != exitOK {
		return code
	}
	_ = dir
	for _, name := range sortedNames(g) {
		p := g.Packages[name]
		fmt.Printf("%s  %s@%s\n", name, p.Repo, shortSHA(p.Commit))
		for _, r := range p.Routes {
			fmt.Printf("  route %s\n", r)
		}
		if len(p.Linker.Libs) > 0 {
			fmt.Printf("  linker -l%s\n", strings.Join(p.Linker.Libs, " -l"))
		}
	}
	return exitOK
}

func cmdWhy(args []string) int {
	if len(args) != 1 {
		fail(errors.New("usage: bojo why <pkg>"))
		return exitUsage
	}
	_, g, code := resolveProject()
	if code != exitOK {
		return code
	}
	p, ok := packageByShortName(g, args[0])
	if !ok {
		p, ok = g.Packages[args[0]]
	}
	if !ok {
		fail(fmt.Errorf("package %q is not part of this build (see `bojo tree`)", args[0]))
		return exitUsage
	}
	fmt.Printf("%s\n", p.Name)
	fmt.Printf("  repo    %s\n", p.Repo)
	fmt.Printf("  commit  %s\n", p.Commit)
	fmt.Printf("  dir     %s\n", p.Dir)
	for _, r := range p.Routes {
		fmt.Printf("  route   %s\n", r)
	}
	return exitOK
}

func cmdVerify() int {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	lf, err := lock.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}
	if len(lf.Packages) == 0 {
		fmt.Println("lock is empty")
		return exitOK
	}
	store := cache.New(dir)
	bad := 0
	for _, e := range lf.Packages {
		if strings.HasPrefix(e.Commit, "path:") {
			fmt.Printf("  skip    %s (local path dependency)\n", e.Name)
			continue
		}
		if err := store.VerifyFresh(e.Repo, e.Commit, e.Tree); err != nil {
			fmt.Printf("  FAILED  %s: %v\n", e.Name, err)
			bad++
			continue
		}
		fmt.Printf("  ok      %s %s\n", e.Name, shortSHA(e.Commit))
	}
	if bad > 0 {
		fmt.Printf("%d package(s) failed verification\n", bad)
		return exitUsage
	}
	fmt.Println("lock verified")
	return exitOK
}

func cmdAllow(args []string) int {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return exitUsage
	}
	allow, err := hooks.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}
	if len(args) == 0 || args[0] == "--list" {
		if len(allow.Entries) == 0 {
			fmt.Println("no approvals")
			return exitOK
		}
		for _, e := range allow.Entries {
			fmt.Printf("%s  %s %s  %s\n", e.ID(), e.Pkg, e.Hook, strings.Join(e.Command, " "))
		}
		return exitOK
	}

	pending, err := readPending(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}
	for _, e := range pending {
		if e.ID() != args[0] {
			continue
		}
		if err := allow.Append(e); err != nil {
			fail(err)
			return exitUsage
		}
		fmt.Printf("approved %s for %s (%s)\n", e.ID(), e.Pkg, e.Hook)
		return exitOK
	}
	fail(fmt.Errorf("no blocked hook with id %q; run a build first, then approve the id it prints", args[0]))
	return exitUsage
}

// ---------------------------------------------------------------------------
// run / install (ephemeral, D34a)

func cmdRun(args []string, install bool) int {
	if len(args) == 0 {
		fail(errors.New("usage: bojo run|install <url>@<pkg>/<version>"))
		return exitUsage
	}
	spec, err := parseSpec(args[0])
	if err != nil {
		fail(err)
		return exitUsage
	}
	force := false
	for _, a := range args[1:] {
		switch a {
		case "--force":
			force = true
		default:
			fail(fmt.Errorf("unknown argument %q", a))
			return exitUsage
		}
	}

	tmp, err := os.MkdirTemp("", "bojo-*")
	if err != nil {
		fail(err)
		return exitUsage
	}
	defer os.RemoveAll(tmp)

	cfgText := fmt.Sprintf(`[project]
name    = "ephemeral"
version = "0.0.0"
entry   = "main.mojo"

[[deps-from]]
repo = %q
tag  = %q
libs = [%q]
`, spec.repo, spec.tag(), spec.pkg)
	if err := os.WriteFile(filepath.Join(tmp, "bojo.toml"), []byte(cfgText), 0o644); err != nil {
		fail(err)
		return exitUsage
	}

	cfg, err := manifest.Load(tmp)
	if err != nil {
		fail(err)
		return exitUsage
	}
	store := cache.New(tmp)
	r := &resolve.Resolver{Store: store}
	g, err := r.Resolve(cfg)
	if err != nil {
		fail(err)
		return resolveExit(err)
	}

	p, ok := packageByShortName(g, spec.pkg)
	if !ok {
		fail(fmt.Errorf("package %q not found", spec.pkg))
		return exitUsage
	}
	if p.Kind != "cmd" {
		fail(fmt.Errorf("package %q is a library; only kind = \"cmd\" packages can be run or installed", spec.pkg))
		return exitUsage
	}

	// The approval store belongs to the invoking project, not to the temporary
	// build directory: otherwise a hook could never be approved at all.
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
		return exitUsage
	}
	allow, err := hooks.Load(cwd)
	if err != nil {
		fail(err)
		return exitUsage
	}
	if code := gate(cwd, allow.Filter(g.LockedCommits()), g); code != exitOK {
		return code
	}

	if _, err := vendor.Build(tmp, g); err != nil {
		fail(err)
		return exitUsage
	}
	modulesDir := ""
	if err != nil {
		fail(err)
		return exitBuild
	}
	entry := filepath.Join(p.Dir, p.Entry)
	out := filepath.Join(tmp, ".out", spec.pkg)
	version := ""
	if cwdCfg, cerr := manifest.Load(cwd); cerr == nil {
		version = cwdCfg.Version.Mojo
	}
	mojoBin, err := toolchain.Resolve(version)
	if err != nil {
		fail(err)
		return exitUsage
	}
	if err := build.Mojo(mojoBin, p.Dir, entry, out, modulesDir, cache.New(p.Dir).Compiler(), vendor.Roots(tmp), build.LinkerArgs(g, p.Dir, nil, nil)); err != nil {
		fail(fmt.Errorf("mojo build failed: %w", err))
		return exitBuild
	}

	if install {
		binDir := binDirPath()
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			fail(err)
			return exitUsage
		}
		dest := filepath.Join(binDir, spec.pkg)
		if _, err := os.Stat(dest); err == nil && !force {
			fail(fmt.Errorf("%s already exists; pass --force to overwrite", dest))
			return exitUsage
		}
		if err := copyFile(out, dest); err != nil {
			fail(err)
			return exitUsage
		}
		fmt.Printf("installed %s\n", dest)
		if !pathContains(binDir) {
			fmt.Printf("note: %s is not in PATH\n", binDir)
			fmt.Printf("      export PATH=\"%s:$PATH\"\n", binDir)
		}
		return exitOK
	}

	cmd := exec.Command(out)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		// The program's own exit code is not bojo's build failure: hand it through.
		if cmd.ProcessState != nil {
			if code := cmd.ProcessState.ExitCode(); code > 0 {
				return code
			}
		}
		return exitBuild
	}
	return exitOK
}

// ---------------------------------------------------------------------------
// helpers

func resolveProject() (string, *resolve.Graph, int) {
	dir, err := projectRoot()
	if err != nil {
		fail(err)
		return "", nil, exitUsage
	}
	cfg, err := manifest.Load(dir)
	if err != nil {
		fail(err)
		return "", nil, exitUsage
	}
	lf, err := lock.Load(dir)
	if err != nil {
		fail(err)
		return "", nil, exitUsage
	}
	store := cache.New(dir)
	r := &resolve.Resolver{Store: store, Locked: lf.TreesByCommit()}
	g, err := r.Resolve(cfg)
	if err != nil {
		fail(err)
		return "", nil, resolveExit(err)
	}
	return dir, g, exitOK
}

type spec struct {
	repo, pkg, version string
	pkgs               []string
}

// tag is what the manifest stores: one version per repository (D74).
func (s spec) tag() string { return "v" + strings.TrimPrefix(s.version, "v") }

// libs is the package selection; --lib may name several.
func (s spec) libs() []string { return s.pkgs }

// pkgTag is the tag that names the package and its version (D79).
func (s spec) pkgTag() string { return s.pkg + "@" + s.tag() }

// parseSpec splits `url#pkg@version` (D80): the URL is the address, the
// fragment selects the package and its version.
func parseSpec(raw string) (spec, error) {
	hash := strings.LastIndex(raw, "#")
	if hash < 0 {
		return spec{}, fmt.Errorf("%q is missing #<pkg>@<version> (example: https://github.com/owner/repo#pkg@v0.0.1)", raw)
	}
	repo, rest := raw[:hash], raw[hash+1:]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return spec{}, fmt.Errorf("%q is missing @<version>", raw)
	}
	pkgName, ver := rest[:at], rest[at+1:]
	if repo == "" || pkgName == "" || ver == "" {
		return spec{}, fmt.Errorf("%q is not a valid package reference", raw)
	}
	return spec{repo: repo, pkg: pkgName, version: ver}, nil
}

func writeLock(dir, mojoFingerprint string, g *resolve.Graph) error {
	lf := &lock.File{Version: 1, Mojo: mojoFingerprint}
	for _, e := range g.Lock {
		lf.Packages = append(lf.Packages, lock.Entry{
			Name:   e.Name,
			Repo:   e.Repo,
			Tag:    e.Tag,
			Commit: e.Commit,
			Tree:   e.Tree,
			Alias:  e.Alias,
		})
	}
	return lock.Save(dir, lf)
}

func writePending(dir string, entries []hooks.Entry) error {
	path := filepath.Join(dir, ".bojo", "pending.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func readPending(dir string) ([]hooks.Entry, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ".bojo", "pending.json"))
	if err != nil {
		return nil, errors.New("no blocked hook to approve; run a build first")
	}
	var entries []hooks.Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func appendDepFrom(dir string, s spec, alias string) error {
	path := filepath.Join(dir, "bojo.toml")
	var b strings.Builder
	b.WriteString("\n[[deps-from]]\n")
	fmt.Fprintf(&b, "repo = %q\n", s.repo)
	fmt.Fprintf(&b, "tag  = %q\n", s.pkgTag())
	libs := s.libs()
	quoted := make([]string, len(libs))
	for i, l := range libs {
		quoted[i] = fmt.Sprintf("%q", l)
	}
	fmt.Fprintf(&b, "libs = [%s]\n", strings.Join(quoted, ", "))
	if alias != "" {
		fmt.Fprintf(&b, "alias = { %s = %q }\n", s.pkg, alias)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return err
}

func sortedNames(g *resolve.Graph) []string {
	names := make([]string, 0, len(g.Packages))
	for n := range g.Packages {
		names = append(names, n)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

func binDirPath() string {
	if v := os.Getenv("BOJO_BIN"); v != "" {
		return v
	}
	return filepath.Join(os.Getenv("HOME"), ".bojo", "bin")
}

// copyFile writes through a temporary name so a crash never leaves a truncated
// binary in ~/.bojo/bin (review note).
func copyFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func pathContains(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == dir {
			return true
		}
	}
	return false
}

func removeIfExists(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	return true, os.RemoveAll(path)
}

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

// fingerprint turns `Mojo 1.2.0 (2016d0cd)` into `1.2.0-2016d0cd` (D35).
func fingerprint(versionOutput string) string {
	return toolchain.ParseFingerprint(versionOutput)
}

// toolchainFingerprint returns `1.2.0-2016d0cd`, or "" when mojo is absent.
func toolchainFingerprint() string {
	out, err := exec.Command("mojo", "--version").Output()
	if err != nil {
		return ""
	}
	return fingerprint(string(out))
}

func mojoVersionOr(fallback string) string {
	out, err := exec.Command("mojo", "--version").Output()
	if err != nil {
		return fallback
	}
	f := fingerprint(string(out))
	if f == "" {
		return fallback
	}
	return strings.SplitN(f, "-", 2)[0]
}

func hostTarget() string {
	arch := "aarch64"
	if runtime.GOARCH == "amd64" {
		arch = "x86_64"
	}
	if runtime.GOOS == "darwin" {
		return arch + "-apple-darwin"
	}
	return arch + "-unknown-linux-gnu"
}
