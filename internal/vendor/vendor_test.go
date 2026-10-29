package vendor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LukasLow/bojo/internal/resolve"
)

func pkgDir(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "__init__.mojo"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBuildIsFlatAndOneDirectoryPerModulePath(t *testing.T) {
	src := t.TempDir()
	project := t.TempDir()

	// Two packages with the same short name from different repositories.
	a := pkgDir(t, src, "markus", "def connect() -> Int:\n    return 111\n")
	b := pkgDir(t, src, "anna", "def connect() -> Int:\n    return 222\n")

	g := &resolve.Graph{Packages: map[string]*resolve.Package{
		"github.com.Markus.mojosocket": {Name: "mojosocket", Path: "github.com.Markus.mojosocket", Dir: a, Commit: strings.Repeat("a", 40)},
		"github.com.Anna.mojosocket":   {Name: "mojosocket", Path: "github.com.Anna.mojosocket", Dir: b, Commit: strings.Repeat("b", 40)},
	}}

	m, err := Build(project, g)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(m.Entries))
	}
	for _, want := range []string{
		"github/com/Markus/mojosocket/__init__.mojo",
		"github/com/Anna/mojosocket/__init__.mojo",
	} {
		if _, err := os.Stat(filepath.Join(project, Dir, filepath.FromSlash(want))); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}

func TestBuildGeneratesAliasShim(t *testing.T) {
	src := t.TempDir()
	project := t.TempDir()
	dir := pkgDir(t, src, "addition", "def add(a: Int, b: Int) -> Int:\n    return a + b\n")

	g := &resolve.Graph{Packages: map[string]*resolve.Package{
		"github.com.LukasLow.bojo_demo_pkg.addition": {
			Name: "addition", Path: "github.com.LukasLow.bojo_demo_pkg.addition",
			Dir: dir, Commit: strings.Repeat("c", 40), Alias: "mathx",
		},
	}}
	if _, err := Build(project, g); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(project, Dir, "mathx", "__init__.mojo"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "from github.com.LukasLow.bojo_demo_pkg.addition import *") {
		t.Fatalf("shim does not re-export the full path:\n%s", got)
	}
}

func TestBuildRejectsOnePathAtTwoCommits(t *testing.T) {
	src := t.TempDir()
	project := t.TempDir()
	dir := pkgDir(t, src, "z", "def label() -> String:\n    return \"z\"\n")

	// The graph keys by path, so this can only be constructed directly.
	g := &resolve.Graph{Packages: map[string]*resolve.Package{
		"github.com.X.z": {Name: "z", Path: "github.com.X.z", Dir: dir, Commit: strings.Repeat("1", 40)},
	}}
	if _, err := Build(project, g); err != nil {
		t.Fatal(err)
	}

	// A second package with the same path but another commit is rejected by the
	// resolver already (g.add); Build keeps its own guard for hand-built graphs.
	g.Packages["other"] = &resolve.Package{Name: "z2", Path: "github.com.X.z", Dir: dir, Commit: strings.Repeat("2", 40)}
	if _, err := Build(project, g); err == nil {
		t.Fatal("two commits for one module path must fail")
	}
}

func TestBuildHonoursExclude(t *testing.T) {
	src := t.TempDir()
	project := t.TempDir()
	dir := pkgDir(t, src, "http", "def get() -> Int:\n    return 1\n")
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "a.mojo"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	g := &resolve.Graph{Packages: map[string]*resolve.Package{
		"github.com.X.http": {Name: "http", Path: "github.com.X.http", Dir: dir, Commit: strings.Repeat("3", 40), Exclude: []string{"tests/**"}},
	}}
	if _, err := Build(project, g); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, Dir, "github/com/X/http/tests/a.mojo")); !os.IsNotExist(err) {
		t.Fatalf("tests/a.mojo must be excluded, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, Dir, "github/com/X/http/__init__.mojo")); err != nil {
		t.Fatalf("package body must be copied: %v", err)
	}
}
