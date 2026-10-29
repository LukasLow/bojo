package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/LukasLow/bojo/internal/resolve"
)

// bundleRuntimeFiles copies every file a package declared as `runtime` into
// `<out>/data/<pkg>/…`. The launcher points BOJO_DATA at that directory, so a
// package can read its data without knowing where it was installed (D60).
func bundleRuntimeFiles(dir string, g *resolve.Graph, outDir string) error {
	modules := resolve.ModulesDir(dir)
	for _, p := range g.Packages {
		if len(p.Linker.Runtime) == 0 {
			continue
		}
		root := p.Name
		if len(p.Routes) > 0 {
			root = p.Routes[0]
		}
		for _, entry := range p.Linker.Runtime {
			src := filepath.Join(modules, root, entry)
			dst := filepath.Join(outDir, "data", p.Name, entry)
			if err := copyTree(src, dst); err != nil {
				return fmt.Errorf("%s: runtime file %q: %w", p.Name, entry, err)
			}
		}
	}
	return nil
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		raw, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, raw, 0o644)
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0o644)
	})
}
