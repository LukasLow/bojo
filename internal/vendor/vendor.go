// Package vendor materialises the resolved graph into a build tree (D65–D67).
//
// Layout:
//
//	vendor/vendor.json          provenance for every route
//	vendor/mojo/pkgs/<route>/   packages the project asked for itself
//	vendor/mojo/subpkgs/<route>_<hash8>/   transitive packages
//
// Transitive routes are content-addressed: the same content gets the same name
// in every project, so two different packages that happen to share a route name
// (Markus/mojosocket and Anna/mojosocket) coexist without touching each other.
// The imports of a dependent are rewritten to the resolved name.
package vendor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LukasLow/bojo/internal/resolve"
)

const (
	// PkgsDir holds the routes the project asked for by name (relative to the
	// staging directory, which becomes vendor/).
	PkgsDir = "mojo/pkgs"
	// SubDir holds transitive routes, always suffixed with their content hash.
	SubDir = "mojo/subpkgs"
	// ManifestName is the provenance file inside vendor/.
	ManifestName = "vendor.json"

	hashLen = 8
)

// Entry is one vendored package.
type Entry struct {
	Route      string   `json:"route"`
	Pkg        string   `json:"pkg"`
	Repo       string   `json:"repo"`
	Tag        string   `json:"tag"`
	Commit     string   `json:"commit"`
	Direct     bool     `json:"direct"`
	RequiredBy []string `json:"required_by,omitempty"`
}

// Manifest is what vendor/vendor.json contains.
type Manifest struct {
	Schema  int     `json:"schema"`
	Entries []Entry `json:"entries"`
}

// Roots are the module roots a build must pass to the compiler.
func Roots(projectDir string) []string {
	return []string{
		filepath.Join(projectDir, "vendor", filepath.FromSlash(PkgsDir)),
		filepath.Join(projectDir, "vendor", filepath.FromSlash(SubDir)),
	}
}

// Build writes the vendor tree for a resolved graph and returns its manifest.
func Build(projectDir string, g *resolve.Graph) (*Manifest, error) {
	order, err := topoOrder(g)
	if err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp(projectDir, ".vendor-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	assigned := map[string]string{} // package name -> vendored route
	byPkg := map[string][]string{}  // package name -> dependents
	for name, p := range g.Packages {
		for _, d := range p.Deps {
			byPkg[d] = append(byPkg[d], name)
		}
	}

	entries := make([]Entry, 0, len(order))
	for _, name := range order {
		p := g.Packages[name]
		if len(p.DeclaredRoutes) != 1 {
			return nil, fmt.Errorf("package %q declares %d routes; vendoring supports one route per package for now",
				name, len(p.DeclaredRoutes))
		}
		old := p.DeclaredRoutes[0]

		// Rewrite the imports this package makes on its own dependencies.
		mapping := map[string]string{}
		for _, d := range p.Deps {
			dep, ok := g.Packages[d]
			if !ok {
				return nil, fmt.Errorf("%s: dependency %q was not resolved", name, d)
			}
			if len(dep.DeclaredRoutes) != 1 {
				return nil, fmt.Errorf("dependency %q of %q declares %d routes", d, name, len(dep.DeclaredRoutes))
			}
			was := dep.DeclaredRoutes[0]
			if now, ok := assigned[d]; ok && now != was {
				mapping[was] = now
			}
		}

		sub := SubDir
		if p.Direct {
			sub = PkgsDir
		}
		target := filepath.Join(tmp, filepath.FromSlash(sub), old)
		if err := copyPackage(p.Dir, target, excludes(p)); err != nil {
			return nil, err
		}
		if err := rewriteTree(target, mapping); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}

		route := old
		if !p.Direct {
			sum, err := hashTree(target)
			if err != nil {
				return nil, err
			}
			route = old + "_" + sum
			final := filepath.Join(tmp, filepath.FromSlash(sub), route)
			if err := os.Rename(target, final); err != nil {
				return nil, err
			}
			target = final
		}
		if err := renameModule(target, old, route); err != nil {
			return nil, err
		}
		assigned[name] = route

		entries = append(entries, Entry{
			Route: route, Pkg: name, Repo: p.Repo, Tag: p.Tag, Commit: p.Commit,
			Direct: p.Direct, RequiredBy: byPkg[name],
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Route < entries[j].Route })
	raw, err := json.MarshalIndent(Manifest{Schema: 1, Entries: entries}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, ManifestName), append(raw, '\n'), 0o644); err != nil {
		return nil, err
	}
	final := filepath.Join(projectDir, "vendor")
	old := final + ".old"
	if err := os.RemoveAll(old); err != nil {
		return nil, err
	}
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Rename(old, final)
		return nil, err
	}
	_ = os.RemoveAll(old)
	return &Manifest{Schema: 1, Entries: entries}, nil
}

// topoOrder returns package names with dependencies before dependents.
func topoOrder(g *resolve.Graph) ([]string, error) {
	names := make([]string, 0, len(g.Packages))
	for n := range g.Packages {
		names = append(names, n)
	}
	sort.Strings(names)

	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var order []string

	var visit func(string) error
	visit = func(n string) error {
		switch state[n] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("dependency cycle through %q", n)
		}
		state[n] = visiting
		p, ok := g.Packages[n]
		if !ok {
			return fmt.Errorf("dependency %q is not part of the graph", n)
		}
		for _, d := range p.Deps {
			if d == n {
				continue
			}
			if err := visit(d); err != nil {
				return err
			}
		}
		state[n] = done
		order = append(order, n)
		return nil
	}
	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func excludes(p *resolve.Package) []string { return p.Exclude }

// copyPackage copies a package directory and skips vcs metadata and the
// manifest's exclude patterns.
func copyPackage(src, dst string, exclude []string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "target") {
			return filepath.SkipDir
		}
		if matchesAny(filepath.ToSlash(rel), exclude) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, info.Mode()|0o400)
	})
}

func matchesAny(rel string, patterns []string) bool {
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
		if strings.HasSuffix(pat, "/**") && strings.HasPrefix(rel, strings.TrimSuffix(pat, "**")) {
			return true
		}
		if strings.HasPrefix(pat, "**/") && strings.HasSuffix(rel, strings.TrimPrefix(pat, "**")) {
			return true
		}
	}
	return false
}

// rewriteTree rewrites import statements in every .mojo file below root.
func rewriteTree(root string, mapping map[string]string) error {
	if len(mapping) == 0 {
		return nil
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".mojo") {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out, changed, err := rewriteSource(string(raw), mapping)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if !changed {
			return nil
		}
		return os.WriteFile(p, []byte(out), 0o644)
	})
}

// rewriteSource rewrites `from X import …` and `import X…` at statement start.
// Anything else that mentions X outside a comment *and outside a string* is an
// error: a half-rewritten copy of foreign code is worse than a clear abort
// (D67).
func rewriteSource(src string, mapping map[string]string) (string, bool, error) {
	lines := strings.Split(src, "\n")
	changed := false
	var triple string

	for i, line := range lines {
		code, next := codeOnly(line, triple)
		triple = next

		trimmed := strings.TrimLeft(code, " \t")
		indent := code[:len(code)-len(trimmed)]

		var keyword string
		switch {
		case strings.HasPrefix(trimmed, "from "):
			keyword = "from "
		case strings.HasPrefix(trimmed, "import "):
			keyword = "import "
		}

		if keyword != "" {
			rest := trimmed[len(keyword):]
			end := strings.IndexAny(rest, " \t")
			if end < 0 {
				end = len(rest)
			}
			route := rest[:end]
			if now, ok := mapping[route]; ok {
				// Rebuild from the code line, then append whatever the original
				// line still carries (string literal, comment).
				lines[i] = indent + keyword + now + rest[end:] + line[len(code):]
				changed = true
			}
			continue
		}

		for was := range mapping {
			if mentionsIdentifier(trimmed, was) {
				return "", false, fmt.Errorf("route %q is used outside an import statement: %q", was, strings.TrimSpace(line))
			}
		}
	}
	return strings.Join(lines, "\n"), changed, nil
}

// codeOnly removes comments and string literals from one line so that a mention
// inside a docstring cannot be mistaken for code. It returns the remaining code
// and the triple-quote state at the end of the line.
func codeOnly(line, triple string) (string, string) {
	var b strings.Builder
	i := 0
	for i < len(line) {
		if triple != "" {
			if strings.HasPrefix(line[i:], triple) {
				i += len(triple)
				triple = ""
				b.WriteString(" ")
				continue
			}
			i++
			continue
		}
		c := line[i]
		switch {
		case c == '#':
			return b.String(), ""
		case c == '"' || c == '\'':
			if strings.HasPrefix(line[i:], strings.Repeat(string(c), 3)) {
				triple = strings.Repeat(string(c), 3)
				i += 3
				b.WriteString(" ")
				continue
			}
			// single-line string
			j := i + 1
			for j < len(line) && line[j] != c {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1
			b.WriteString(" ")
			continue
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), triple
}

// mentionsIdentifier reports whether s contains name as a standalone token.
func mentionsIdentifier(s, name string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], name)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !isIdent(s[j-1])
		afterIdx := j + len(name)
		after := afterIdx >= len(s) || !isIdent(s[afterIdx])
		if before && after {
			return true
		}
		i = j + 1
	}
}

func isIdent(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// hashTree hashes paths and contents in a stable order.
func hashTree(root string) (string, error) {
	type file struct {
		rel  string
		data []byte
	}
	var files []file
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, file{filepath.ToSlash(rel), data})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%d\x00", f.rel, len(f.data))
		h.Write(f.data)
	}
	return hex.EncodeToString(h.Sum(nil))[:hashLen], nil
}

// renameModule keeps the vendored manifest honest about the route it serves.
func renameModule(dir, from, to string) error {
	if from == to {
		return nil
	}
	p := filepath.Join(dir, "bojo.pkg.toml")
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil // a package without a manifest is allowed
	}
	out := strings.ReplaceAll(string(raw), `"`+from+`"`, `"`+to+`"`)
	if out == string(raw) {
		return nil
	}
	return os.WriteFile(p, []byte(out), 0o644)
}
