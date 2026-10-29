package tag

import "testing"

func TestParseAndCompare(t *testing.T) {
	a, err := ParseTag("addition/v0.0.1", "addition")
	if err != nil {
		t.Fatal(err)
	}
	if a.String() != "v0.0.1" {
		t.Fatalf("String() = %q", a.String())
	}

	b, err := ParseTag("addition/v0.0.10", "addition")
	if err != nil {
		t.Fatal(err)
	}
	if b.Compare(a) <= 0 {
		t.Fatal("v0.0.10 should sort after v0.0.1")
	}

	pre, err := ParseVersion("v1.0.0-rc.1")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := ParseVersion("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if pre.Compare(rel) >= 0 {
		t.Fatal("a pre-release must sort before its release")
	}
}

func TestParseTagRejectsForeignTags(t *testing.T) {
	for _, raw := range []string{
		"v0.0.1",                // no package prefix
		"hello/v0.0.1",          // other package
		"addition/sub/v0.0.1",   // two slashes
		"addition/v0.0",         // not semver
		"addition/0.0.1",        // no leading v
		"addition/v0.0.1/extra", // extra segment
	} {
		if _, err := ParseTag(raw, "addition"); err == nil {
			t.Errorf("ParseTag(%q) should fail", raw)
		}
	}
}

func TestLatestIgnoresForeignTags(t *testing.T) {
	tags := []string{
		"v9.9.9",
		"hello/v5.0.0",
		"addition/v0.0.1",
		"addition/v0.0.3",
		"addition/v0.0.2",
	}
	got, err := Latest("addition", tags)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "v0.0.3" {
		t.Fatalf("Latest = %q, want v0.0.3", got.String())
	}
}

func TestMatch(t *testing.T) {
	tags := []string{"addition/v0.0.1", "addition/v0.0.2"}

	if v, err := Match("addition", "latest", tags); err != nil || v.String() != "v0.0.2" {
		t.Fatalf("latest: %v %v", v, err)
	}
	if v, err := Match("addition", "v0.0.1", tags); err != nil || v.String() != "v0.0.1" {
		t.Fatalf("exact with v: %v %v", v, err)
	}
	if v, err := Match("addition", "0.0.1", tags); err != nil || v.String() != "v0.0.1" {
		t.Fatalf("exact without v: %v %v", v, err)
	}
	if _, err := Match("addition", "v0.0.9", tags); err == nil {
		t.Fatal("unknown version should fail")
	}
	if _, err := Match("addition", "main", tags); err == nil {
		t.Fatal("a branch ref must be rejected")
	}
}
