# bojo — Agent Skill

> `bojo skill` prints this text. Save it as a skill with
> `bojo skill > .agents/skills/bojo/SKILL.md` (or pipe it through `cat`).
> It describes only what exists in bojo v0.1.

## When this skill applies

Use it whenever a project contains a `bojo.toml`, whenever someone mentions
`bojo build`, `bojo run`, `bojo install` or `bojo get`, or whenever a Mojo program
needs to be built that spans more than one file.

## What bojo is

bojo is a package manager and build driver for Mojo. It solves three problems
`mojo build` alone does not:

1. **Dependencies** — Mojo resolves imports purely through `-I` paths. There is
   no manifest, no registry, no version in the import.
2. **Linker flags** — `.mojoc` packages carry no link metadata. Anyone binding a
   C library would otherwise have to hand their flags to every consumer.
3. **Distribution** — a `mojo build` result depends on an absolute RUNPATH into
   the build environment. It does not start on a foreign machine.

## The four files

| File | Role | Committed? |
|---|---|---|
| `bojo.toml` | what **this** project needs | yes |
| `bojo.pkg.toml` | what **one package** offers (one per directory, `import` points at directory paths) | yes |
| `bojo.lock` | the resolved commits (the truth) | yes |
| `bojo.allow` | approved hooks (JSONL) | yes |
| `.bojo/` | cache, module root, build state | no (gitignored) |

## Commands

```
bojo init                     create a project
bojo get <url>#<pkg>@<ver>    add a dependency (--as <name> on a collision)
bojo remove <pkg>             remove one
bojo update [pkg]             re-resolve
bojo build [--locked]         build into out/
bojo run <url>#<pkg>@<ver>    ephemeral build and run a remote command
bojo install <url>#<pkg>@<ver>  ephemeral build → ~/.bojo/bin
bojo test | fmt | doc         delegate to the Mojo tools
bojo tree | why <pkg>         graph / provenance
bojo verify                   check the lock against a fresh checkout
bojo doctor                   toolchain, C compiler, triple, PATH
bojo cache list|clear         show / clear the project cache
bojo allow <id>|--json|--list approve or list hooks
bojo clean [--all]            remove .bojo/ (--all: also out/)
bojo skill                    print this text
```

Exit codes an agent must react to:

| Code | Meaning | Correct reaction |
|---|---|---|
| 0 | success | — |
| 1 | usage or configuration error | read the manifest, quote the message, do **not** guess |
| 2 | **blocked by a missing hook approval — nothing was executed** | show the user the proposal; **never** approve it yourself |
| 3 | build or link failure | `bojo doctor`, then the build log |
| 4 | network or clone failure | retry, or `--offline` |
| 5 | lock conflict (`--locked`) | suggest `bojo update`; do not run it automatically |

## Adding a dependency

```bash
bojo get https://github.com/LukasLow/example_pkg#addition@v0.1.0
```

That writes into `bojo.toml`:

```toml
[[deps-from]]
repo  = "https://github.com/LukasLow/example_pkg"
tag   = "addition/v0.1.0"
libs  = ["addition"]
```

Rules an agent must know:

- One repository can offer **several packages**, and each package has its own
  tag. Therefore there is one `[[deps-from]]` table per (repo, version) — not one
  per repo.
- The version is the tag `pkg/vX.Y.Z`; `@latest` picks the highest semver under
  `<pkg>/*`. A commit pin (`@9f3a2b1c`) is allowed and must resolve uniquely.
- Two packages with the same import route are an **error**, not a choice. On a
  collision use `--as <name>`.
- One route belongs to exactly one package. Mojo has no version mangling; two
  versions of the same library cannot coexist in one build.

## Building

```bash
bojo build
```

What happens, in order:

1. Validate the manifest, resolve dependencies, fetch missing repos into
   `.bojo/cache/src` and verify the git tree against `bojo.lock`.
2. Build `.bojo/modules`: one symlink per route into the cache.
3. Hook gate: every foreign hook needs a line in `bojo.allow`.
4. Pre-build hooks, then precompile, then `mojo build` with exactly one `-I`.
5. Post-processing: `strip`, library closure, relative RPATH, optional
   single-file stub.

Your own Mojo source is **never** modified. The import name is the route, not the
package name — with an alias you write `from sum import add`.

## Hooks

A hook is an external process a package wants to run during a build — typically a
code generator (`templ generate`), a C/C++ build (`cargo build`, `cmake`), or
binding generation.

Shape inside the package:

```toml
[pkg.web_templ.hooks]
pre-build  = [["templ", "generate"]]
post-build = []
```

Behaviour:

- Hooks are **argv arrays**. No shell, no script file, no pipes. If you need
  pipes, write a program.
- `cwd` is the package directory; hooks **may** write there.
- **Never automatic.** Without an approval the build ends with exit 2 and one
  line naming `bojo allow <id>`. Nothing is executed.
- **An agent never approves on its own.** It shows the user the repo, commit,
  package and command, and waits. There is no `-y`.
- Hooks of your own project (`[hooks]` in your own `bojo.toml`) need no approval.

## Output

```toml
[build]
output = "dir"        # default: out/ with binary + lib/
# output = "onefile"  # single file, native stub, +2–3 ms startup
strip  = true         # strip libstdc++: 26.4 MB → 5.29 MB
```

What an agent must say about the limits instead of improvising:

- **There is no static ELF.** Mojo ships only dynamic runtime libraries
  (`modular/modular#1317`). `output = "static"` does not exist.
- **`libstdc++` cannot be linked in.** All three Mojo runtime libraries link it
  themselves; static linking would change nothing.
- **A bundle needs the runtime libraries next to the binary** (`lib/` plus a
  relative RPATH) or the single-file stub. A bare `mojo build` binary only runs
  on the build machine.
- **Python interop needs Python on the target** unless the project opts in to
  bundling. `mojo build` never embeds Python:

  ```toml
  [python]
  version  = "3.12"
  packages = ["numpy==2.1.0"]
  ```

  With `[python]` set, bojo ships a relocatable interpreter and the wheels with
  the artifact (a launcher stub sets `PYTHONHOME`/`PYTHONPATH`). Without it, the
  target machine must provide Python 3.10–3.14 and the packages. A Python-capable
  bundle is roughly ten times larger than a pure Mojo one; say that out loud
  instead of silently producing a binary that cannot import numpy.

## Typical errors

| Message | Cause | What to do |
|---|---|---|
| `unable to find suitable c compiler for linking` | no C driver on the host | `bojo doctor`; let the user install a toolchain — **do not** install packages on your own |
| `error while loading shared libraries: libKGENCompilerRTShared.so` | binary shipped without the runtime libraries | use `output = "dir"` or `onefile` |
| `blocked: pkg … wants to run a hook` | hook without approval | let the user decide (exit 2) |
| `conflict: route 'x' provided by …` | two packages, same import route | set `--as`, or remove one dependency |
| `lock mismatch` (exit 5) | `bojo.lock` does not match the manifest | suggest `bojo update` |
| `mojoc version mismatch` | toolchain differs from the one used to build | set `[version] mojo` to the matching version |

## What an agent must not do

- Never write a `bojo.allow` line by hand. Always through `bojo allow`, always
  after asking the user.
- Never commit `.bojo/` and never treat it as a vendor directory.
- Never invent linker flags. If a package needs flags, they belong in its
  `[linker]` section.
- Never run `mojo build` around bojo when a `bojo.toml` exists — `-I`, RPATH and
  the closure would all be missing.
- Never hand-edit a version in the lock. The lock is generated.
