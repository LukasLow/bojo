package resolve

import "testing"

func mkpkg(path, commit string) Package {
	return Package{Name: "request", Path: path, Repo: "https://github.com/x/y", Commit: commit, Dir: "/tmp/x"}
}

func TestAddRejectsOnePathAtTwoCommits(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	if err := g.add(mkpkg("github.com.x.y.request", "1111111111111111111111111111111111111111"), nil); err != nil {
		t.Fatal(err)
	}
	err := g.add(mkpkg("github.com.x.y.request", "2222222222222222222222222222222222222222"), nil)
	if err == nil {
		t.Fatal("two commits for one module path must fail")
	}
}

func TestAddIsIdempotentForTheSameCommit(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	p := mkpkg("github.com.x.y.request", "1111111111111111111111111111111111111111")
	if err := g.add(p, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.add(p, nil); err != nil {
		t.Fatalf("merging the same package twice must be a no-op: %v", err)
	}
	if len(g.Packages) != 1 {
		t.Fatalf("packages = %d, want 1", len(g.Packages))
	}
}

func TestAddAppliesAliasToTheRoute(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	p := mkpkg("github.com.LukasLow.bojo_demo_pkg.request", "1111111111111111111111111111111111111111")
	if err := g.add(p, map[string]string{"request": "http"}); err != nil {
		t.Fatal(err)
	}
	if len(g.Routes) != 1 || g.Routes[0].Path != "http" {
		t.Fatalf("routes = %+v, want one route named http", g.Routes)
	}
	if g.Packages[p.Path].Alias != "http" {
		t.Fatalf("alias not recorded: %+v", g.Packages[p.Path])
	}
}

func TestAddRecordsTheModulePathInTheLock(t *testing.T) {
	g := &Graph{Packages: map[string]*Package{}}
	p := mkpkg("github.com.LukasLow.bojo_demo_pkg.request", "1111111111111111111111111111111111111111")
	if err := g.add(p, nil); err != nil {
		t.Fatal(err)
	}
	if len(g.Lock) != 1 || g.Lock[0].Name != p.Path {
		t.Fatalf("lock entry = %+v, want the module path", g.Lock)
	}
	if g.Lock[0].Commit != p.Commit {
		t.Fatalf("lock must carry the full commit, got %q", g.Lock[0].Commit)
	}
}
