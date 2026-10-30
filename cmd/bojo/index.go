package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/LukasLow/bojo/internal/manifest"
	"github.com/LukasLow/bojo/internal/toolchain"
	"github.com/LukasLow/bojo/internal/urlkey"
	"github.com/LukasLow/bojo/internal/vendor"
)

// ---------------------------------------------------------------------------
// bojo repo-index: read manifests over raw HTTP, never clone.

type indexEntry struct {
	Path        string   `json:"path"`
	Pkg         string   `json:"pkg"`
	Dir         string   `json:"dir"`
	Version     string   `json:"version"`
	Tag         string   `json:"tag"`
	Description string   `json:"description,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	License     string   `json:"license,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	FFI         bool     `json:"ffi,omitempty"`
	Runtime     bool     `json:"runtime,omitempty"`
	Hooks       []string `json:"hooks,omitempty"`
	Readme      string   `json:"readme,omitempty"`
	Docs        string   `json:"docs,omitempty"`
	Deps        []string `json:"deps,omitempty"`
}

// rawURL builds the raw-file URL for a forge. Unknown hosts use the Gitea
// pattern, which is the most common self-hosted one.
func rawURL(norm, ref, path string) (string, error) {
	host, repoPath := splitRepo(norm)
	switch host {
	case "github.com":
		return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", repoPath, ref, path), nil
	case "gitlab.com":
		return fmt.Sprintf("https://gitlab.com/%s/-/raw/%s/%s", repoPath, ref, path), nil
	default:
		return fmt.Sprintf("https://%s/%s/raw/%s/%s", host, repoPath, ref, path), nil
	}
}

func splitRepo(norm string) (host, repoPath string) {
	rest := strings.TrimPrefix(norm, "https://")
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return rest[:i], strings.TrimSuffix(rest[i+1:], ".git")
	}
	return rest, ""
}

func fetchRaw(norm, ref, path string) ([]byte, error) {
	u, err := rawURL(norm, ref, path)
	if err != nil {
		return nil, err
	}
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func cmdRepoIndex(args []string) int {
	if len(args) == 0 {
		fail(fmt.Errorf("usage: bojo repo-index <url>[@ref] [--follow]"))
		return exitUsage
	}
	raw := args[0]
	follow := false
	ref := ""
	if i := strings.LastIndex(raw, "@"); i > 0 {
		raw, ref = raw[:i], raw[i+1:]
	}
	for _, a := range args[1:] {
		switch a {
		case "--follow":
			follow = true
		default:
			fail(fmt.Errorf("unknown flag %q", a))
			return exitUsage
		}
	}

	norm, err := urlkey.Normalize(raw)
	if err != nil {
		fail(err)
		return exitUsage
	}

	// Tags name their package (D79); the highest one per package wins.
	perPkg := map[string]string{}
	if ref == "" {
		perPkg, err = tagsByPackage(raw)
		if err != nil {
			fail(err)
			return exitNetwork
		}
	}

	base := ref
	if base == "" {
		base = "HEAD"
	}
	sha, err := refSHA(raw, base)
	if err != nil {
		fail(err)
		return exitNetwork
	}

	entries, err := indexRepo(norm, raw, sha, ref, perPkg, follow, 0)
	if err != nil {
		fail(err)
		return exitNetwork
	}
	enc := json.NewEncoder(os.Stdout)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			fail(err)
			return exitUsage
		}
	}
	return exitOK
}

// tagsByPackage maps a package name to its highest version tag.
func tagsByPackage(raw string) (map[string]string, error) {
	out, err := exec.Command("git", "ls-remote", "--tags", "--refs", raw).Output()
	if err != nil {
		return nil, fmt.Errorf("listing tags of %s: %w", raw, err)
	}
	best := map[string]tagVersion{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		t := strings.TrimPrefix(fields[1], "refs/tags/")
		at := strings.IndexByte(t, '@')
		if at <= 0 {
			continue
		}
		pkg, ver := t[:at], t[at+1:]
		cand := tagVersion{raw: t, version: ver}
		if prev, ok := best[pkg]; !ok || cand.version > prev.version {
			best[pkg] = cand
		}
	}
	res := map[string]string{}
	for pkg, tv := range best {
		res[pkg] = tv.raw
	}
	return res, nil
}

type tagVersion struct {
	raw     string
	version string
}

// indexRepo reads the root manifest, then every package manifest at the commit
// that its own tag points to.
func indexRepo(norm, raw, sha, forced string, perPkg map[string]string, follow bool, depth int) ([]indexEntry, error) {
	body, err := fetchRaw(norm, sha, "bojo.pkg.toml")
	if err != nil {
		return nil, fmt.Errorf("%s has no readable bojo.pkg.toml: %w", norm, err)
	}
	var root struct {
		Import []string `toml:"import"`
	}
	if err := toml.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", norm, err)
	}

	type linker struct {
		Libs    []string `toml:"libs"`
		FFI     []string `toml:"ffi"`
		Runtime []string `toml:"runtime"`
	}
	type pkgEntry struct {
		Modules     []string              `toml:"modules"`
		Description string                `toml:"description"`
		Keywords    []string              `toml:"keywords"`
		License     string                `toml:"license"`
		Homepage    string                `toml:"homepage"`
		Readme      string                `toml:"readme"`
		Docs        string                `toml:"docs"`
		Hooks       map[string][][]string `toml:"hooks"`
		Linker      linker                `toml:"linker"`
		DepsFrom    []struct {
			Repo string   `toml:"repo"`
			Libs []string `toml:"libs"`
		} `toml:"deps-from"`
	}
	var out []indexEntry
	cache := map[string]string{}

	for _, imp := range root.Import {
		imp = strings.TrimSuffix(strings.TrimPrefix(imp, "./"), "/")
		path := filepath.ToSlash(filepath.Join(imp, "bojo.pkg.toml"))
		rawManifest, err := fetchRaw(norm, sha, path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", norm, err)
		}
		var probe struct {
			Pkg map[string]pkgEntry `toml:"pkg"`
		}
		if err := toml.Unmarshal(rawManifest, &probe); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", norm, path, err)
		}

		for name := range probe.Pkg {
			version := forced
			realTag := forced
			atSHA := sha
			if version == "" {
				if tag, ok := perPkg[name]; ok {
					realTag = tag
					atSHA, err = refSHA(raw, tag)
					if err != nil {
						return nil, err
					}
					version = tag[strings.IndexByte(tag, '@')+1:]
					if atSHA != sha {
						if m, err := fetchRaw(norm, atSHA, path); err == nil {
							rawManifest = m
							_ = toml.Unmarshal(m, &probe)
						}
					}
				} else {
					version = "unreleased"
				}
			}
			p := probe.Pkg[name]
			modPath, err := urlkey.ModulePath(norm, name)
			if err != nil {
				return nil, err
			}
			var deps []string
			for _, d := range p.DepsFrom {
				dn, err := urlkey.Normalize(d.Repo)
				if err != nil {
					continue
				}
				for _, lib := range d.Libs {
					if mp, err := urlkey.ModulePath(dn, lib); err == nil {
						deps = append(deps, mp)
					}
				}
			}
			out = append(out, indexEntry{
				Path: modPath, Pkg: name, Dir: imp, Version: version, Tag: realTag,
				Description: p.Description, Keywords: p.Keywords, License: p.License,
				Homepage: p.Homepage, Readme: p.Readme, Docs: p.Docs,
				Hooks: hookNames(p.Hooks), FFI: len(p.Linker.FFI) > 0,
				Runtime: len(p.Linker.Runtime) > 0, Deps: deps,
			})
			if follow && depth < 1 {
				for _, d := range p.DepsFrom {
					dn, err := urlkey.Normalize(d.Repo)
					if err != nil {
						continue
					}
					if _, seen := cache[dn]; seen {
						continue
					}
					cache[dn] = "1"
					subTags, err := tagsByPackage(d.Repo)
					if err != nil {
						continue
					}
					subHead, err := refSHA(d.Repo, "HEAD")
					if err != nil {
						continue
					}
					nested, err := indexRepo(dn, d.Repo, subHead, "", subTags, false, depth+1)
					if err != nil {
						continue
					}
					out = append(out, nested...)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// hookNames lists the hook kinds a package declares, sorted.
func hookNames(h map[string][][]string) []string {
	var names []string
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// refSHA resolves a tag or branch to its commit without cloning.
func refSHA(raw, ref string) (string, error) {
	for _, candidate := range []string{"refs/tags/" + ref + "^{}", "refs/tags/" + ref, "refs/heads/" + ref, ref} {
		out, err := exec.Command("git", "ls-remote", raw, candidate).Output()
		if err != nil {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(string(out)))
		if len(fields) >= 1 && len(fields[0]) == 40 {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("cannot resolve %q in %s", ref, raw)
}

// latestTag asks the remote for its tags; no clone.
func latestTag(raw string) (string, error) {
	out, err := exec.Command("git", "ls-remote", "--tags", "--refs", raw).Output()
	if err != nil {
		return "", fmt.Errorf("listing tags of %s: %w", raw, err)
	}
	var best string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		tag := strings.TrimPrefix(fields[1], "refs/tags/")
		if strings.Contains(tag, "/") {
			continue // a per-package tag is not a repository version
		}
		if best == "" || tag > best {
			best = tag
		}
	}
	if best == "" {
		return "", fmt.Errorf("%s has no tags", raw)
	}
	return best, nil
}

// ---------------------------------------------------------------------------
// bojo search

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	d, _ := os.UserHomeDir()
	return d
}

func indexPath() string {
	if p := os.Getenv("BOJO_INDEX"); p != "" {
		return p
	}
	return filepath.Join(homeDir(), ".bojo", "index.jsonl")
}

func cmdSearch(args []string) int {
	if len(args) == 0 {
		fail(fmt.Errorf("usage: bojo search <term> [--index <file>] [--json]"))
		return exitUsage
	}
	term := strings.ToLower(args[0])
	path := indexPath()
	asJSON := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--index":
			if i+1 >= len(args) {
				fail(fmt.Errorf("--index needs a path"))
				return exitUsage
			}
			path = args[i+1]
			i++
		default:
			fail(fmt.Errorf("unknown flag %q", args[i]))
			return exitUsage
		}
	}

	f, err := os.Open(path)
	if err != nil {
		fail(fmt.Errorf("no index at %s (run `bojo repo-index <url> > %s`)", path, path))
		return exitUsage
	}
	defer f.Close()

	var hits []indexEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e indexEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		hay := strings.ToLower(e.Path + " " + e.Description + " " + strings.Join(e.Keywords, " "))
		if strings.Contains(hay, term) {
			hits = append(hits, e)
		}
	}
	if err := sc.Err(); err != nil {
		fail(err)
		return exitUsage
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		for _, h := range hits {
			_ = enc.Encode(h)
		}
		return exitOK
	}
	for _, h := range hits {
		fmt.Printf("%s  %s\n", h.Path, h.Version)
		if h.Description != "" {
			fmt.Printf("  %s\n", h.Description)
		}
		fmt.Printf("  bojo get %s@%s --lib %s\n", repoOf(h.Path), h.Version, lastSegment(h.Path))
	}
	if len(hits) == 0 {
		fmt.Printf("no match for %q in %s\n", term, path)
	}
	return exitOK
}

func repoOf(modulePath string) string {
	parts := strings.Split(modulePath, ".")
	if len(parts) < 4 {
		return modulePath
	}
	return "https://" + strings.Join(parts[:len(parts)-1], "/")
}

func lastSegment(modulePath string) string {
	if i := strings.LastIndexByte(modulePath, '.'); i >= 0 {
		return modulePath[i+1:]
	}
	return modulePath
}

// ---------------------------------------------------------------------------
// bojo channel

func channelFile() string { return filepath.Join(homeDir(), ".bojo", "channels.json") }

func loadChannels() []string {
	raw, err := os.ReadFile(channelFile())
	if err != nil {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	return list
}

func saveChannels(list []string) error {
	if err := os.MkdirAll(filepath.Dir(channelFile()), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(channelFile(), append(raw, '\n'), 0o644)
}

func cmdChannel(args []string) int {
	list := loadChannels()
	if len(args) == 0 || args[0] == "list" {
		if len(list) == 0 {
			fmt.Println("no channels (add one with `bojo channel add <url>`)")
			return exitOK
		}
		for _, c := range list {
			fmt.Println(c)
		}
		return exitOK
	}
	switch args[0] {
	case "add":
		if len(args) < 2 {
			fail(fmt.Errorf("usage: bojo channel add <url>"))
			return exitUsage
		}
		for _, c := range list {
			if c == args[1] {
				fmt.Println("already added")
				return exitOK
			}
		}
		list = append(list, args[1])
	case "remove":
		if len(args) < 2 {
			fail(fmt.Errorf("usage: bojo channel remove <url>"))
			return exitUsage
		}
		kept := list[:0]
		for _, c := range list {
			if c != args[1] {
				kept = append(kept, c)
			}
		}
		list = kept
	default:
		fail(fmt.Errorf("usage: bojo channel [list|add <url>|remove <url>]"))
		return exitUsage
	}
	if err := saveChannels(list); err != nil {
		fail(err)
		return exitUsage
	}
	fmt.Printf("%d channel(s)\n", len(list))
	return exitOK
}

// ---------------------------------------------------------------------------
// bojo validate: manifests parse, the graph resolves, .vendor matches.

func cmdValidate(args []string) int {
	dir, g, code := resolveProject()
	if code != exitOK {
		return code
	}
	if err := vendor.Compare(dir, g); err != nil {
		fail(err)
		return exitLockFile
	}
	fmt.Printf("ok: %d package(s), manifests and .vendor agree\n", len(g.Packages))
	return exitOK
}

// ---------------------------------------------------------------------------
// bojo test: run every _tests/*.mojo with the pinned toolchain.

func cmdTest(args []string) int {
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
	mojoBin, err := toolchain.Resolve(cfg.Version.Mojo)
	if err != nil {
		fail(err)
		return exitUsage
	}

	var files []string
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".bojo" || d.Name() == ".vendor" || d.Name() == ".out") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".mojo") && filepath.Base(filepath.Dir(p)) == "_tests" {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		fail(err)
		return exitUsage
	}
	if len(files) == 0 {
		fmt.Println("no _tests/*.mojo found")
		return exitOK
	}
	sort.Strings(files)

	failed := 0
	for _, f := range files {
		cmd := exec.Command(mojoBin, "run", f, "-I", ".", "-I", vendor.Dir)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			failed++
			fmt.Printf("FAIL %s\n%s\n", f, strings.TrimSpace(string(out)))
			continue
		}
		fmt.Printf("ok   %s\n", f)
	}
	if failed > 0 {
		fmt.Printf("\n%d of %d failed\n", failed, len(files))
		return exitBuild
	}
	fmt.Printf("\n%d passed\n", len(files))
	return exitOK
}
