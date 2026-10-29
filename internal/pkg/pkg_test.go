package pkg

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const rootWithImports = `
import = ["lib/a", "lib/b"]

[repo]
url     = "https://github.com/Owner/Repo"
license = "Apache-2.0"
`

func TestLoadTreeDiamondIsAllowed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", `
import = ["lib/a", "lib/b"]

[repo]
url = "https://github.com/Owner/Repo"
`)
	write(t, dir, "lib/a/bojo.pkg.toml", `
import = ["lib/common"]
[pkg.a]
modules = ["a"]
`)
	write(t, dir, "lib/b/bojo.pkg.toml", `
import = ["lib/common"]
[pkg.b]
modules = ["b"]
`)
	write(t, dir, "lib/common/bojo.pkg.toml", `
[pkg.common]
modules = ["common"]
`)

	tree, err := LoadTree(dir)
	if err != nil {
		t.Fatalf("diamond import must be allowed: %v", err)
	}
	if len(tree.Packages) != 3 {
		t.Fatalf("got %d packages, want 3", len(tree.Packages))
	}
	if tree.Packages["a"].Dir != filepath.Join(dir, "lib/a") {
		t.Fatalf("package dir = %q", tree.Packages["a"].Dir)
	}
}

func TestLoadTreeRejectsCycle(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", rootWithImports)
	write(t, dir, "lib/a/bojo.pkg.toml", `
import = ["lib/b"]
[pkg.a]
modules = ["a"]
`)
	write(t, dir, "lib/b/bojo.pkg.toml", `
import = ["lib/a"]
[pkg.b]
modules = ["b"]
`)
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("cycle must be rejected")
	}
}

func TestLoadTreeRejectsDuplicatePackage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", rootWithImports)
	write(t, dir, "lib/a/bojo.pkg.toml", `
[pkg.same]
modules = ["a"]
`)
	write(t, dir, "lib/b/bojo.pkg.toml", `
[pkg.same]
modules = ["b"]
`)
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("duplicate package name must be rejected")
	}
}

func TestLoadTreeRejectsParentTraversal(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", `
import = ["../outside"]

[repo]
url = "https://github.com/Owner/Repo"

[pkg.a]
modules = ["a"]
`)
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("import with .. must be rejected")
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", `
[repo]
url = "https://github.com/Owner/Repo"
surprise = 1
[pkg.a]
modules = ["a"]
`)
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("unknown keys must be rejected")
	}
}

func TestLibraryNeedsModules(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "bojo.pkg.toml", `
[repo]
url = "https://github.com/Owner/Repo"
[pkg.a]
`)
	if _, err := LoadTree(dir); err == nil {
		t.Fatal("a library without modules must be rejected")
	}
}
