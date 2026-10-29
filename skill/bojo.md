# bojo — Agent Skill

> `bojo skill` prints this text. Save it as a skill with
> `bojo skill > .agents/skills/bojo/SKILL.md`.
> It describes only what exists today. If the CLI and this text disagree, the
> CLI is right — say so instead of guessing.

## When this skill applies

Any project containing a `bojo.toml`, any mention of `bojo build`, `bojo run`,
`bojo get`, or any Mojo program spanning more than one file.

## What bojo is

A package manager and build driver for Mojo. It solves three problems that
`mojo build` alone does not:

1. **Dependencies** — Mojo resolves imports through a flat `-I` list. There is
   no manifest, no version in the import, and two packages with the same short
   name cannot coexist.
2. **Native code** — a package that binds C or Rust needs linker flags and the
   sources compiled; nothing carries that information.
3. **Distribution** — a `mojo build` result points at an absolute RUNPATH into
   the build environment and does not start on another machine.

Not provided, deliberately: no precompiled artefacts (`.mojoc` buys nothing,
measured), no static linking (Mojo cannot), no automatic hooks.

## Files

| File | Role | Committed |
|---|---|---|
| `bojo.toml` | what this project needs (schema 1) | yes |
| `bojo.pkg.toml` | repository root: `import = [...]`; per package: `[pkg.<name>]` | yes |
| `bojo.lock` | every dependency by module path, with repo, tag, full commit, tree | yes |
| `bojo.allow` | approved hooks (JSONL) | yes |
| `.vendor/` | generated module root, one directory per module path | **no** |
| `.bojo/` | cache, toolchains lock, build state | **no** |
| `.out/` | build result | **no** |

## Import paths

A package is imported by its full module path:

```mojo
from github.com.LukasLow.bojo_demo_pkg.cprint import banner
```

`<host>.<owner>.<repo>.<package>` — independent of where the author stores the
directory. Libraries always write this full path; only the end user may shorten
it, and only in `bojo.toml`:

```toml
[alias]
cprint = "printer"
```

```mojo
from printer import banner   # bojo generates .vendor/printer/__init__.mojo
```

## Versions

One version per repository, as a plain tag (`v0.0.1`). `libs = [...]` selects
which packages of that repository are used. `<pkg>/vX.Y.Z` tags still resolve
for older repositories. Two different commits for the same module path are an
error — bojo never upgrades silently.

## Commands

```
bojo init                        create a project
bojo get <url>@<version>         add a dependency (--as <name> on a collision)
bojo remove <pkg>                remove one
bojo update [pkg]                re-resolve and write bojo.lock
bojo vendor [--check]            write or verify .vendor
bojo build [--locked]            build into .out/
bojo run <url>@<version>         ephemeral build and run a remote command
bojo install <url>@<version>     ephemeral build -> ~/.bojo/bin
bojo tree | why <pkg>            graph / provenance
bojo verify                      check the lock against a fresh checkout
bojo doctor                      toolchain, C compiler, triple, PATH
bojo toolchain [list|install|remove] <mojo|python> [version]
bojo deps list|add|add-python    edit dependencies
bojo cache list|clear            show / clear the project cache
bojo allow <id>|--json|--list    approve or list hooks
bojo clean [--all]               remove .bojo/ (--all: also .out/ and .vendor/)
bojo skill                       print this text
```

Exit codes an agent must react to:

| Code | Meaning | Reaction |
|---|---|---|
| 0 | success | — |
| 1 | usage or configuration error | quote the message, do not guess |
| 2 | blocked by a missing hook approval, **nothing ran** | show the user the proposal; never approve it yourself |
| 3 | build or link failure | `bojo doctor`, then the build log |
| 4 | network or clone failure | retry |
| 5 | lock or vendor conflict (`--locked`, `vendor --check`) | suggest `bojo update` / `bojo vendor`; do not run it yourself |

## Adding a dependency

```bash
bojo get https://github.com/LukasLow/example_pkg@v0.1.0
```

```toml
[[deps-from]]
repo = "https://github.com/LukasLow/example_pkg"
tag  = "v0.1.0"
libs = ["addition"]
```

## Native code (C and Rust)

The package ships sources and names them under `ffi`; the **consumer** compiles
them in a `pre-build` hook. A library never runs a compiler on someone else's
machine.

```toml
[pkg.cprint]
modules = ["cprint"]

[pkg.cprint.linker]
libs  = ["cprint"]
ffi   = ["cprint.c"]
```

```toml
[linker]
libdirs = [".bojo/native"]
libs    = ["cprint"]

[hooks]
pre-build = [
  ["sh", "-c", "mkdir -p .bojo/native && cc -shared -fPIC -O2 -o .bojo/native/libcprint.so .vendor/github/com/Owner/repo/cprint/cprint.c"],
]
```

Anything that produces a C ABI works: Rust needs `crate-type = ["cdylib"]` and
`#[no_mangle] pub extern "C"`.

## Runtime data

Files a binary needs at run time (tables, JSON, models) are declared as
`runtime` and copied into `.out/data/<package>/`. The launcher exports
`BOJO_DATA` pointing at that directory; the program joins the relative path.

```toml
[pkg.randomname.linker]
runtime = ["names.txt"]
```

```toml
[pkg.http]
modules = ["http"]
exclude = ["tests/**", "docs/**"]
```

`exclude` filters what reaches `.vendor/`; the cache keeps the whole repository.

## Python

```toml
[version]
mojo   = "1.2.0"
python = "3.12"

[deps.python]
numpy = "2.1.0"
```

`bojo toolchain install python 3.12` once. Then `bojo build` downloads wheels
only (an sdist is refused), hashes them, installs offline and copies the
interpreter into `.out/python/`. The launcher sets `PYTHONHOME`, `PYTHONPATH`
and `MOJO_PYTHON_LIBRARY`.

## Hooks

A hook is an argv array, never a shell string, never automatic. It runs only
when `bojo.allow` contains a line for exactly that (repo, commit, package,
hook, command). `bojo build` prints the pending approval and exits 2 without
executing anything.

## Build output

| `[build] output` | Result |
|---|---|
| `dir` (default) | `.out/<name>` + `.out/lib/` + `.out/python/` + `.out/data/` |
| `onefile` | one self-extracting file in `.out/`; first start unpacks into `~/.cache/bojo/run/<hash>/` |

Both carry the runtime closure with a relative RPATH (`$ORIGIN/lib`), so the
directory or the single file runs on another machine with the same platform.

## For an agent

- Never invent a flag. `bojo --help` and the subcommand help are authoritative.
- Never approve hooks on the user's behalf.
- After a change to `bojo.toml`, run `bojo build` once and report the first
  error verbatim.
- `bojo why <pkg>` answers "why is this in my tree", `bojo tree` shows the shape.
