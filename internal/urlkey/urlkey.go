// Package urlkey normalises repository URLs and derives stable cache keys.
//
// Normalisation is deliberately component-based (DESIGN §7, D14a): string
// replacement gets the scp form `git@host:owner/repo` wrong, because the `git@`
// looks like userinfo.
package urlkey

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// Normalize returns the canonical form of a repository URL and rejects URLs
// that carry credentials (D25).
func Normalize(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("empty repository URL")
	}
	// A fragment is never part of the repository identity.
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}

	// scp-like form: git@host:owner/repo(.git)
	if !strings.Contains(s, "://") {
		if at := strings.LastIndex(s, "@"); at >= 0 {
			rest := s[at+1:]
			if colon := strings.Index(rest, ":"); colon >= 0 {
				host := rest[:colon]
				path := strings.TrimSuffix(rest[colon+1:], "/")
				s = "https://" + host + "/" + path
			}
		}
	}

	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid repository URL %q: %w", raw, err)
	}
	if u.User != nil {
		// `git@` is the conventional SSH user, not a credential; anything with a
		// password, or a different user on a plain HTTP(S) URL, is rejected (D25).
		_, hasPassword := u.User.Password()
		user := strings.ToLower(u.User.Username())
		if hasPassword || (user != "git" && user != "") {
			return "", fmt.Errorf("repository URL must not contain credentials")
		}
		u.User = nil
	}
	if u.Host == "" {
		return "", fmt.Errorf("repository URL %q has no host", raw)
	}

	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "git", "ssh":
		scheme = "https"
	case "http":
		scheme = "https"
	case "https":
	default:
		if scheme == "" {
			scheme = "https"
		} else {
			return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
		}
	}

	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "443" || port == "80" || port == "22" {
		port = ""
	}
	if strings.Contains(host, ":") { // IPv6
		host = "[" + host + "]"
	}
	if port != "" {
		host = host + ":" + port
	}

	path := u.Path
	path = strings.TrimSuffix(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, ".GIT")
	path = strings.ReplaceAll(path, "//", "/")
	if path == "" {
		return "", fmt.Errorf("repository URL %q has no path", raw)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return scheme + "://" + host + path, nil
}

// Hash returns the 12-hex cache key for a normalised URL.
// Path turns a normalised URL into a filesystem path without scheme, i.e.
// https://github.com/LukasLow/mojolib -> github.com/LukasLow/mojolib (D63).
func Path(normalized string) (string, error) {
	u, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("%q has no host", normalized)
	}
	p := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	if p == "" {
		return "", fmt.Errorf("%q has no repository path", normalized)
	}
	return u.Host + "/" + p, nil
}

func Hash(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:12]
}
