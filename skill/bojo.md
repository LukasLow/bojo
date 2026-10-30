# bojo — Agent Skill

> `bojo skill` prints this text. Save it as `bojo skill > .agents/skills/bojo/SKILL.md`.
> It describes only what exists. If the CLI and this text disagree, the CLI wins —
> say so instead of guessing.

## When this skill applies

Any project with a `bojo.toml`, any mention of `bojo build`, `run`, `get`, or any
Mojo program spanning more than one file.

## What bojo is

A package manager and build driver for Mojo. It solves what `mojo build` cannot:
dependencies with identity and versions, native code (C/Rust) that has to be
compiled and linked, and a result that runs on another machine.

Deliberately absent: no precompiled artefacts, no static linking, no automatic
hooks.

## Files

| File | Role | Committed |
|---|---|---|
| `bojo.toml` | what this project needs (`schema = 1`) | yes |
| `bojo.pkg.toml` | repo root: `import = [...]`; per package: `[pkg.<name>]` | yes |
| `bojo.lock` | every dependency by module path with repo, tag, full commit, tree | yes |
| `bojo.allow` | approved hooks (JSONL) | yes |
| `.vendor/` | generated module root, one directory per module path | no |
| `.bojo/` | cache, toolchains, build state | no |
| `.out/` | build result | no |

## Import paths

```mojo
from github.com.LukasLow.bojo_demo_pkg.cprint import banner
```

`<host>.<owner>.<repo>.<package>` — where the author keeps the directory does not
appear. Libraries always write the full path; only the project may alias, and
only in `bojo.toml`:

```toml
[deps-from."https://github.com/LukasLow/bojo_demo_pkg"]
cprint   = { version = "v0.0.1", as = "printer" }
addition = "v0.0.2"
```

```mojo
from printer import banner     # bojo generates .vendor/printer/__init__.mojo
```

## Versions

One version per package, as a tag `<pkg>@vX.Y.Z` (`tls@v0.0.1`). The section is
keyed by repository, each key is a package, each value its version;
`"latest"` or a missing value means the highest tag. Two different commits for
the same module path are an error — bojo never upgrades silently.

## Commands

```
bojo init                        create a project
bojo get <url>#<pkg>@<version>    add a dependency (--as <name> to alias)
bojo remove <pkg>                delete it from its repository table
bojo update                      re-resolve and write bojo.lock
bojo build [--locked]            build into .out/
bojo run <url>#<pkg>@<version>    ephemeral build and run a command package
bojo install <url>#<pkg>@<version>  ephemeral build -> ~/.bojo/bin
bojo vendor [--check]            write or verify .vendor
bojo validate                    manifests + graph + .vendor must agree
bojo test                        run every _tests/*.mojo, exit 3 on failure
bojo fmt [paths…]                run `mojo format`
bojo docs <pkg> [path]           render docs/index.md or a file inside docs/
bojo tree | why <pkg>            graph / provenance
bojo verify                      check the lock against a fresh checkout
bojo doctor                      toolchain, C compiler, triple, PATH
bojo toolchain list|install|remove <mojo|python> [version]
bojo deps list|add|add-python    edit dependencies
bojo repo-index <url>[@ref] [--follow]   read manifests over raw HTTP, emit JSONL
bojo search <term> [--index <file>] [--json]
bojo channel list|add <url>|remove <url>
bojo cache list|clear            show / clear the project cache
bojo allow <id>|--json|--list    approve or list hooks
bojo clean [--all]               remove .bojo/ (--all: also .out/ and .vendor/)
bojo skill                       print this text
```

Exit codes:

| Code | Meaning | Reaction |
|---|---|---|
| 0 | success | — |
| 1 | usage or configuration error | quote the message, do not guess |
| 2 | blocked by a missing hook approval, **nothing ran** | show the user the proposal; never approve it yourself |
| 3 | build, link or test failure | `bojo doctor`, then the log |
| 4 | network or clone failure | retry |
| 5 | lock or vendor conflict (`--locked`, `vendor --check`) | suggest `bojo update` / `bojo vendor`; do not run it yourself |

## Native code

The package ships sources under `ffi`; the **consumer** compiles them in a
`pre-build` hook. A library never runs a compiler on someone else's machine.

```toml
[pkg.cprint]
modules = ["cprint"]

[pkg.cprint.linker]
libs = ["cprint"]
ffi  = ["cprint.c"]
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

Rust works the same way: `crate-type = ["cdylib"]`, `#[unsafe(no_mangle)] pub extern "C"`.

## Runtime data

```toml
[pkg.randomname.linker]
runtime = ["names.txt"]
```

Copied to `.out/data/<package>/`; the launcher exports `BOJO_DATA`.

## Python

```toml
[version]
mojo   = "1.2.0"
python = "3.12"

[deps.python]
numpy = "2.1.0"
```

`bojo toolchain install python 3.12` once. Wheels only, hashed, installed
offline into `.bojo/cache/python/…`; the launcher sets `PYTHONHOME`,
`PYTHONPATH`, `MOJO_PYTHON_LIBRARY`.

## Output

`dir` (default): `.out/<name>` + `lib/` + `python/` + `data/`.
`onefile`: one self-extracting file; first start unpacks into
`~/.cache/bojo/run/<hash>/` and marks it, later starts run directly.

## Rules an agent must never break

- Never invent a flag; `bojo --help` and the subcommand are authoritative.
- Never approve a hook on the user's behalf, and never add to `bojo.allow`.
- After a manifest change, run `bojo build` once and quote the first error.
- `bojo why <pkg>` explains why something is in the tree; `bojo tree` shows the shape.
