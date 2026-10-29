package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/LukasLow/bojo/internal/build"
	"github.com/LukasLow/bojo/internal/cache"
	"github.com/LukasLow/bojo/internal/hooks"
	"github.com/LukasLow/bojo/internal/manifest"
	"github.com/LukasLow/bojo/internal/onefile"
	"github.com/LukasLow/bojo/internal/pyenv"
	"github.com/LukasLow/bojo/internal/resolve"
	"github.com/LukasLow/bojo/internal/vendor"
)

// buildOnefile builds into a work directory and packs the result into a single
// self-extracting file (D53).
func buildOnefile(dir string, cfg *manifest.File, g *resolve.Graph, mojoBin string) int {
	allow, err := hooks.Load(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}
	lockedCommits := g.LockedCommits()
	if code := gate(dir, allow.Filter(lockedCommits), g); code != exitOK {
		return code
	}

	release, err := build.Lock(dir)
	if err != nil {
		fail(err)
		return exitUsage
	}
	defer release()

	if _, err := vendor.Build(dir, g); err != nil {
		fail(err)
		return exitUsage
	}
	modulesDir := ""
	includes := append(vendor.Roots(dir), cfg.Build.Include...)
	if err := runHooks(cfg, g, "pre-build", dir); err != nil {
		fail(err)
		return exitBuild
	}

	work := filepath.Join(dir, ".bojo", "onefile")
	if err := os.RemoveAll(work); err != nil {
		fail(err)
		return exitBuild
	}
	payload := filepath.Join(work, cfg.Project.Name)
	args := append(build.RuntimeRPATH(), build.LinkerArgs(g, dir, cfg.Linker.LibDirs, cfg.Linker.Libs)...)
	if err := build.Mojo(mojoBin, dir, cfg.Project.Entry, payload, modulesDir, cache.New(dir).Compiler(), includes, args); err != nil {
		fail(fmt.Errorf("mojo build failed: %w", err))
		return exitBuild
	}
	if cfg.Build.Strip {
		_ = build.Strip(payload)
	}
	if _, err := build.Bundle(payload); err != nil {
		fail(fmt.Errorf("bundling runtime libraries: %w", err))
		return exitBuild
	}

	if err := bundleRuntimeFiles(dir, g, work); err != nil {
		fail(err)
		return exitBuild
	}
	trees := map[string]string{"lib": filepath.Join(work, "lib")}
	if _, err := os.Stat(filepath.Join(work, "data")); err == nil {
		trees["data"] = filepath.Join(work, "data")
	}
	if cfg.Version.Python != "" {
		env, err := pyenv.Ensure(dir, cfg.Version.Python, cfg.Deps.Python)
		if err != nil {
			fail(err)
			return exitUsage
		}
		trees["python/runtime"] = env.PythonHome
		trees["python/site-packages"] = env.SitePackages
	}

	final := build.BinaryPath(dir, cfg.Project.Name)
	if err := onefile.Pack(payload, filepath.Join(work, "lib"), trees, cfg.Version.Python, final); err != nil {
		fail(err)
		return exitBuild
	}
	if err := runHooks(cfg, g, "post-build", dir); err != nil {
		fail(err)
		return exitBuild
	}
	fmt.Printf("ok: %s (single file)\n", final)
	return exitOK
}
