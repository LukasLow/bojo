package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/LukasLow/bojo/internal/build"
	"github.com/LukasLow/bojo/internal/hooks"
	"github.com/LukasLow/bojo/internal/manifest"
	"github.com/LukasLow/bojo/internal/onefile"
	"github.com/LukasLow/bojo/internal/resolve"
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

	modulesDir, err := build.Materialize(dir, g.Routes)
	if err != nil {
		fail(err)
		return exitUsage
	}
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
	args := append(build.LinkerArgs(g), build.RuntimeRPATH()...)
	if err := build.Mojo(mojoBin, dir, cfg.Project.Entry, payload, modulesDir, args); err != nil {
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

	final := build.BinaryPath(dir, cfg.Project.Name)
	if err := onefile.Pack(payload, filepath.Join(work, "lib"), final); err != nil {
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
