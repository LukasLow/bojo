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

func TestPackageMetadataIsOptional(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "bojo.pkg.toml")
	if err := os.WriteFile(root, []byte("schema = 1\nimport = [\"lib/request\"]\n\n[repo]\nurl = \"https://github.com/x/y\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "lib", "request"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `schema = 1

[pkg.request]
modules     = ["request"]
description = "HTTP GET through libcurl"
keywords    = ["http", "network"]
license     = "Apache-2.0"
homepage    = "https://example.com"
readme      = "README.md"
`
	if err := os.WriteFile(filepath.Join(dir, "lib", "request", "bojo.pkg.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	tree, err := LoadTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := tree.Packages["request"]
	if p.Description != "HTTP GET through libcurl" {
		t.Errorf("description = %q", p.Description)
	}
	if len(p.Keywords) != 2 || p.Keywords[0] != "http" {
		t.Errorf("keywords = %v", p.Keywords)
	}
	if p.License != "Apache-2.0" || p.Homepage != "https://example.com" || p.Readme != "README.md" {
		t.Errorf("metadata not parsed: %+v", p)
	}
}
