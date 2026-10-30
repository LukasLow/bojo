// Package tag implements the `<pkg>/vX.Y.Z` tag grammar and resolution rules
// (DESIGN §6, D6).
package tag

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Version is a parsed semantic version. Build metadata is ignored for ordering.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
	Raw                 string
}

// String returns the version without build metadata.
func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare orders versions; a pre-release sorts before its release.
func (v Version) Compare(o Version) int {
	if c := cmpInt(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpInt(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpInt(v.Patch, o.Patch); c != 0 {
		return c
	}
	switch {
	case v.Pre == "" && o.Pre == "":
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	default:
		return strings.Compare(v.Pre, o.Pre)
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// ParseTag parses `<pkg>/vX.Y.Z[-pre]` for the given package name.
// A tag without the package prefix, or with more than one slash, is rejected.
func ParseTag(raw, pkg string) (Version, error) {
	for _, sep := range []string{pkg + "@", pkg + "/"} {
		if strings.HasPrefix(raw, sep) {
			rest := strings.TrimPrefix(raw, sep)
			if strings.ContainsAny(rest, "/@") {
				return Version{}, fmt.Errorf("tag %q has more than one separator", raw)
			}
			return ParseVersion(rest)
		}
	}
	return Version{}, fmt.Errorf("tag %q does not start with %q", raw, pkg+"@")
}

// ParseVersion parses `vX.Y.Z[-pre]`.
func ParseVersion(s string) (Version, error) {
	if !strings.HasPrefix(s, "v") {
		return Version{}, fmt.Errorf("version %q must start with 'v'", s)
	}
	body := s[1:]
	pre := ""
	if i := strings.IndexByte(body, '-'); i >= 0 {
		pre = body[i+1:]
		body = body[:i]
	}
	parts := strings.Split(body, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("version %q must be vMAJOR.MINOR.PATCH", s)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("version %q has a non-numeric component %q", s, p)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre, Raw: s}, nil
}

// versionOf reads a tag either as `<pkg>/vX.Y.Z` or as a repository-wide
// `vX.Y.Z` (D74). Both forms carry one version for the whole repository.
func versionOf(raw, pkg string) (Version, bool) {
	if v, err := ParseTag(raw, pkg); err == nil {
		return v, true
	}
	if v, err := ParseVersion(raw); err == nil {
		return v, true
	}
	return Version{}, false
}

// Find returns the tag name and version that `ref` selects.
func Find(pkg, ref string, tags []string) (string, Version, error) {
	type candidate struct {
		raw string
		v   Version
	}
	var cands []candidate
	for _, t := range tags {
		if v, ok := versionOf(t, pkg); ok {
			cands = append(cands, candidate{t, v})
		}
	}
	if len(cands) == 0 {
		return "", Version{}, fmt.Errorf("no version tags for package %q", pkg)
	}

	switch ref {
	case "", "latest":
		sort.Slice(cands, func(i, j int) bool { return cands[i].v.Compare(cands[j].v) > 0 })
		return cands[0].raw, cands[0].v, nil
	case "main", "master":
		return "", Version{}, fmt.Errorf("branch %q is not a version; pin a tag or a commit", ref)
	}

	normalised := ref
	if !strings.HasPrefix(normalised, "v") {
		normalised = "v" + normalised
	}
	want, err := ParseVersion(normalised)
	if err != nil {
		return "", Version{}, err
	}
	for _, c := range cands {
		if c.v.Compare(want) == 0 {
			return c.raw, c.v, nil
		}
	}
	return "", Version{}, fmt.Errorf("package %q has no version %s", pkg, want)
}

// Latest returns the highest version under `<pkg>/*`.
func Latest(pkg string, tags []string) (Version, error) {
	var versions []Version
	for _, t := range tags {
		if v, ok := versionOf(t, pkg); ok {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return Version{}, fmt.Errorf("no version tags for package %q", pkg)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Compare(versions[j]) > 0 })
	return versions[0], nil
}

// Match resolves a user reference ("latest", "v0.0.1", "0.0.1") for a package.
func Match(pkg, ref string, tags []string) (Version, error) {
	switch ref {
	case "", "latest":
		return Latest(pkg, tags)
	case "main", "master":
		return Version{}, fmt.Errorf("branch %q is not a version; pin a tag or a commit", ref)
	}
	normalised := ref
	if !strings.HasPrefix(normalised, "v") {
		normalised = "v" + normalised
	}
	want, err := ParseVersion(normalised)
	if err != nil {
		return Version{}, err
	}
	for _, t := range tags {
		if v, ok := versionOf(t, pkg); ok && v.Compare(want) == 0 {
			return v, nil
		}
	}
	return Version{}, fmt.Errorf("package %q has no version %s", pkg, want)
}
