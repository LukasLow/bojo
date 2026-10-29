package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/LukasLow/bojo/internal/manifest"
	"github.com/LukasLow/bojo/internal/onefile"
	"github.com/LukasLow/bojo/internal/pyenv"
)

// bundlePython provisions the interpreter + wheels and turns the built binary
// into a launcher/ payload pair, so the runtime can set PYTHONHOME,
// PYTHONPATH and MOJO_PYTHON_LIBRARY before Mojo starts (D45/D51).
func bundlePython(dir string, cfg *manifest.File, out string) int {
	res, err := pyenv.Ensure(dir, cfg.Version.Python, cfg.Deps.Python)
	if err != nil {
		fail(err)
		return exitUsage
	}
	if err := pyenv.CopyTree(res, filepath.Dir(out)); err != nil {
		fail(fmt.Errorf("copying the python environment: %w", err))
		return exitBuild
	}

	payload := out + ".bin"
	_ = os.Remove(payload)
	if err := os.Rename(out, payload); err != nil {
		fail(err)
		return exitBuild
	}
	if err := onefile.Launcher(out, filepath.Base(payload), cfg.Version.Python); err != nil {
		fail(err)
		return exitBuild
	}
	fmt.Printf("bundled python %s with %d package(s)\n", cfg.Version.Python, len(cfg.Deps.Python))
	return exitOK
}
