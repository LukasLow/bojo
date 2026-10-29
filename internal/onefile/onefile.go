// Package onefile turns a directory build into a single self-extracting file.
//
// The stub is compiled at build time with the same C compiler Mojo needs for
// linking, so no extra toolchain is required. At start it extracts the payload
// into <cache>/bojo/run/<hash>/ and execs the real binary with a relative library
// path (D53).
package onefile

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

const stubSource = `
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/stat.h>
#include <stdint.h>
#ifdef __APPLE__
#include <mach-o/dyld.h>
#endif

static int self_path(char *buf, size_t n) {
#ifdef __APPLE__
    uint32_t sz = (uint32_t)n;
    if (_NSGetExecutablePath(buf, &sz) != 0) return -1;
    return 0;
#else
    ssize_t r = readlink("/proc/self/exe", buf, n - 1);
    if (r < 0) return -1;
    buf[r] = '\0';
    return 0;
#endif
}

static int mkdirs(const char *path) {
    char tmp[4096];
    snprintf(tmp, sizeof tmp, "%s", path);
    for (char *p = tmp + 1; *p; p++) {
        if (*p == '/') { *p = '\0'; mkdir(tmp, 0755); *p = '/'; }
    }
    return mkdir(tmp, 0755);
}

int main(int argc, char **argv) {
    char self[4096];
    if (self_path(self, sizeof self) != 0) { perror("self"); return 1; }

    FILE *f = fopen(self, "rb");
    if (!f) { perror("open self"); return 1; }
    if (fseek(f, 0, SEEK_END) != 0) return 1;
    long size = ftell(f);
    if (size < 24) { fprintf(stderr, "bojo: truncated payload\n"); return 1; }

    unsigned char trailer[24];
    fseek(f, size - 24, SEEK_SET);
    if (fread(trailer, 1, 24, f) != 24) return 1;
    if (memcmp(trailer + 16, "BOJOPAY1", 8) != 0) {
        fprintf(stderr, "bojo: no payload (is this a plain binary?)\n");
        return 1;
    }
    uint64_t offset = 0;
    memcpy(&offset, trailer, 8);
    char hash[9];
    memcpy(hash, trailer + 8, 8);
    hash[8] = '\0';

    const char *base = getenv("XDG_CACHE_HOME");
    char dir[4096];
    if (base && *base) {
        snprintf(dir, sizeof dir, "%s/bojo/run/%s", base, hash);
    } else {
        const char *home = getenv("HOME");
        if (!home) home = "/tmp";
        snprintf(dir, sizeof dir, "%s/.cache/bojo/run/%s", home, hash);
    }

    if (mkdirs(dir) != 0 && access(dir, F_OK) != 0) { perror("mkdir"); return 1; }

    fseek(f, (long)offset, SEEK_SET);
    char path[4352];
    while (ftell(f) < size - 24) {
        uint32_t nlen = 0, dlen = 0;
        if (fread(&nlen, 4, 1, f) != 1) break;
        if (fread(&dlen, 4, 1, f) != 1) break;
        char name[256];
        if (nlen == 0 || nlen >= sizeof name) return 1;
        if (fread(name, 1, nlen, f) != nlen) return 1;
        name[nlen] = '\0';
        snprintf(path, sizeof path, "%s/%s", dir, name);
        /* create the parent directories only: mkdirs() would otherwise turn the
           file name itself into a directory */
        char parent[4352];
        snprintf(parent, sizeof parent, "%s", path);
        char *slash = strrchr(parent, '/');
        if (slash) { *slash = '\0'; mkdirs(parent); }
        FILE *out = fopen(path, "wb");
        if (!out) { perror(path); return 1; }
        char buf[65536];
        uint32_t left = dlen;
        while (left) {
            size_t chunk = left > sizeof buf ? sizeof buf : left;
            if (fread(buf, 1, chunk, f) != chunk) return 1;
            if (fwrite(buf, 1, chunk, out) != chunk) return 1;
            left -= (uint32_t)chunk;
        }
        fclose(out);
        chmod(path, 0755);
    }
    fclose(f);

    snprintf(path, sizeof path, "%s/lib", dir);
#ifdef __APPLE__
    setenv("DYLD_LIBRARY_PATH", path, 1);
#else
    setenv("LD_LIBRARY_PATH", path, 1);
#endif

    char app[4352];
    snprintf(app, sizeof app, "%s/app.bin", dir);
    execv(app, argv);
    perror("execv");
    return 127;
}
`

type entry struct {
	name string
	data []byte
}

// Pack builds `outPath` from the binary and its library directory.
func Pack(binaryPath, libDir, outPath string) error {
	entries, err := collect(binaryPath, libDir)
	if err != nil {
		return err
	}

	var payload []byte
	for _, e := range entries {
		var hdr [8]byte
		binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(e.name)))
		binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(e.data)))
		payload = append(payload, hdr[:]...)
		payload = append(payload, e.name...)
		payload = append(payload, e.data...)
	}

	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:4])

	stub, err := buildStub()
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(stub))
	stubBytes, err := os.ReadFile(stub)
	if err != nil {
		return err
	}

	tmp := outPath + ".tmp"
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	write := func(b []byte) error {
		_, werr := f.Write(b)
		return werr
	}
	if err := write(stubBytes); err != nil {
		f.Close()
		return err
	}
	if err := write(payload); err != nil {
		f.Close()
		return err
	}

	var trailer [24]byte
	binary.LittleEndian.PutUint64(trailer[0:8], uint64(len(stubBytes)))
	copy(trailer[8:16], hash)
	copy(trailer[16:24], "BOJOPAY1")
	if err := write(trailer[:]); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, outPath)
}

func collect(binaryPath, libDir string) ([]entry, error) {
	app, err := os.ReadFile(binaryPath)
	if err != nil {
		return nil, err
	}
	entries := []entry{{name: "app.bin", data: app}}

	files, err := os.ReadDir(libDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		names = append(names, f.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(libDir, name))
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry{name: "lib/" + name, data: raw})
	}
	return entries, nil
}

// buildStub compiles the launcher once per build with the host C compiler.
func buildStub() (string, error) {
	dir, err := os.MkdirTemp("", "bojo-stub-*")
	if err != nil {
		return "", err
	}
	src := filepath.Join(dir, "stub.c")
	if err := os.WriteFile(src, []byte(stubSource), 0o644); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "stub")
	cmd := exec.Command("cc", "-O2", "-o", out, src)
	if combined, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("compiling the launcher: %v: %s", err, string(combined))
	}
	return out, nil
}
