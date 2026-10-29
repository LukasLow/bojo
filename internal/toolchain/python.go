package toolchain

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// PythonRoot is ~/.bojo/python.
func PythonRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".bojo", "python"), nil
}

// PythonDir is the install directory for one Python version.
func PythonDir(version string) (string, error) {
	root, err := PythonRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "v"+strings.TrimPrefix(version, "v")), nil
}

// PythonInstalled returns the interpreter path for a version.
func PythonInstalled(version string) (string, bool) {
	dir, err := PythonDir(version)
	if err != nil {
		return "", false
	}
	for _, rel := range []string{"bin/python3", "bin/python"} {
		p := filepath.Join(dir, rel)
		if info, err := os.Stat(p); err == nil && info.Mode()&0o111 != 0 {
			return p, true
		}
	}
	return "", false
}

// PythonList returns installed Python runtimes.
func PythonList() ([]string, error) {
	root, err := PythonRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		v := strings.TrimPrefix(e.Name(), "v")
		if _, ok := PythonInstalled(v); ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// PythonInstall downloads a relocatable CPython (python-build-standalone) for
// this platform. The result is a self-contained tree that survives relocation,
// which is what D44 requires.
func PythonInstall(version string) (string, error) {
	version = strings.TrimPrefix(version, "v")
	if p, ok := PythonInstalled(version); ok {
		return p, nil
	}
	if versions, err := PythonList(); err == nil {
		if resolved, ok := matchInstalled(version, versions); ok {
			if p, ok := PythonInstalled(resolved); ok {
				return p, nil
			}
		}
	}

	url, err := standaloneURL(version)
	if err != nil {
		return "", err
	}
	dest, err := PythonDir(version)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "bojo-python-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	archive := filepath.Join(tmp, "python.tar.gz")
	if err := httpGet(url, archive); err != nil {
		return "", err
	}
	unpacked := filepath.Join(tmp, "unpacked")
	if err := untarGz(archive, unpacked); err != nil {
		return "", err
	}
	// The archive contains a single top-level directory (usually "python/").
	inner := unpacked
	if entries, err := os.ReadDir(unpacked); err == nil && len(entries) == 1 && entries[0].IsDir() {
		inner = filepath.Join(unpacked, entries[0].Name())
	}

	_ = os.RemoveAll(dest)
	if err := os.Rename(inner, dest); err != nil {
		return "", err
	}
	p, ok := PythonInstalled(version)
	if !ok {
		return "", fmt.Errorf("installed Python %s but no interpreter was found in %s", version, dest)
	}
	return p, nil
}

// standaloneURL finds the matching python-build-standalone release asset.
func standaloneURL(version string) (string, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/astral-sh/python-build-standalone/releases/latest")
	if err != nil {
		return "", fmt.Errorf("querying python-build-standalone: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("python-build-standalone API: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}

	triple := pythonTriple()
	prefix := "cpython-" + version + "."
	for _, a := range payload.Assets {
		name := a.Name
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if !strings.Contains(name, triple) || !strings.HasSuffix(name, "install_only.tar.gz") {
			continue
		}
		return a.URL, nil
	}
	return "", fmt.Errorf("no python-build-standalone build for %s/%s at version %s", runtime.GOOS, runtime.GOARCH, version)
}

func pythonTriple() string {
	arch := "x86_64"
	if runtime.GOARCH == "arm64" {
		arch = "aarch64"
	}
	if runtime.GOOS == "darwin" {
		return arch + "-apple-darwin"
	}
	return arch + "-unknown-linux-gnu"
}

func httpGet(url, dest string) error {
	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func untarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("archive member %q escapes the destination", hdr.Name)
		}
		info := hdr.FileInfo()
		switch {
		case info.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode()|0o400)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
}
