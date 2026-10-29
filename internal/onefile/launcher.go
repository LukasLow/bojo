package onefile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// launcherSource is the no-payload variant: it stays next to `python/` and the
// real binary, sets the interpreter variables and execs the binary.
//
// A binary cannot set its own environment, which is why this stub exists at all
// (D45/D51).
const launcherSource = `
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/stat.h>
#ifdef __APPLE__
#include <mach-o/dyld.h>
#endif

static int self_dir(char *buf, size_t n) {
#ifdef __APPLE__
    uint32_t sz = (uint32_t)n;
    if (_NSGetExecutablePath(buf, &sz) != 0) return -1;
#else
    ssize_t r = readlink("/proc/self/exe", buf, n - 1);
    if (r < 0) return -1;
    buf[r] = '\0';
#endif
    char *slash = strrchr(buf, '/');
    if (!slash) return -1;
    *slash = '\0';
    return 0;
}

int main(int argc, char **argv) {
    char dir[4096];
    if (self_dir(dir, sizeof dir) != 0) { perror("self"); return 1; }

    char path[4352];
    snprintf(path, sizeof path, "%s/python/runtime", dir);
    setenv("PYTHONHOME", path, 1);
    snprintf(path, sizeof path, "%s/python/site-packages", dir);
    setenv("PYTHONPATH", path, 1);

    /* MOJO_PYTHON_LIBRARY is the primary lookup; pick the first existing name. */
    const char *libs[] = {
        "%s/python/runtime/lib/libpythonPYVER.so.1.0",
        "%s/python/runtime/lib/libpythonPYVER.so",
        "%s/python/runtime/lib/libpythonPYVER.dylib",
    };
    for (unsigned i = 0; i < sizeof libs / sizeof libs[0]; i++) {
        snprintf(path, sizeof path, libs[i], dir);
        if (access(path, R_OK) == 0) { setenv("MOJO_PYTHON_LIBRARY", path, 1); break; }
    }

    char app[4352];
    snprintf(app, sizeof app, "%s/PAYLOAD", dir);
    execv(app, argv);
    perror("execv");
    return 127;
}
`

// Launcher writes a stub that prepares the Python environment and execs
// payloadName from the same directory.
func Launcher(outPath, payloadName, pythonVersion string) error {
	if pythonVersion == "" {
		return fmt.Errorf("launcher needs a python version")
	}
	if strings.ContainsAny(payloadName, "\"\\") {
		return fmt.Errorf("payload name %q cannot be embedded", payloadName)
	}
	src := strings.ReplaceAll(launcherSource, "PYVER", pythonVersion)
	src = strings.ReplaceAll(src, "PAYLOAD", payloadName)

	dir, err := os.MkdirTemp("", "bojo-launcher-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	cFile := filepath.Join(dir, "launcher.c")
	if err := os.WriteFile(cFile, []byte(src), 0o644); err != nil {
		return err
	}
	bin := filepath.Join(dir, "launcher")
	if combined, err := exec.Command("cc", "-O2", "-o", bin, cFile).CombinedOutput(); err != nil {
		return fmt.Errorf("compiling the launcher: %v: %s", err, strings.TrimSpace(string(combined)))
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		return err
	}
	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, outPath)
}
