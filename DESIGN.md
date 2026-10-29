# bojo — Specification v0.1

Status: round 3. Normative. Any semantic change requires a decision in §16
(`D<n>`) including the rejected alternative. Open review findings:
`REVIEW-FINDINGS.md`.

---

## 1. Purpose

bojo is a package manager and build driver for Mojo:

```
bojo get     <url>#<pkg>@<version>   add a dependency
bojo build                           build the project
bojo run     <url>#<pkg>@<version>   build and run a remote command
bojo install <url>#<pkg>@<version>   install a command globally
```

Core idea: **a library author declares in one place what is needed to build and
link.** The consumer types no linker flags.

## 2. Non-goals

- **No static ELF.** Mojo cannot do it (`modular/modular#1317`). `output = "static"`
  does not exist.
- **No prebuilt binaries.** `run`/`install` fetch source and build locally.
- **No global object cache** in v0.1 (D32). Everything lives in the project. The
  single exception is the **toolchain store** under `~/.bojo/mojo/<version>/`
  (D43): toolchains are expensive, identical for every project and useless to
  duplicate.
- **No own registry server.**
- **No second target language** (D34). Other languages appear only as hooks.
- **No mandatory vendor directory.**
- **Windows** is not supported in v0.1 (Mojo does not support it natively).

## 3. Why bojo exists

Measured on Debian bookworm, Mojo 1.2.0 (2016d0cd), aarch64. Numbers: §18.

1. `mojo build hello.mojo` produces a 72 KB ELF that needs
   `libKGENCompilerRTShared.so`, with an **absolute** RUNPATH into the build
   machine's environment. It does not start on a foreign machine.
2. Transitively: `libMSupportGlobals.so`, `libAsyncRTRuntimeGlobals.so`,
   `libstdc++.so.6`, `libgcc_s.so.1`. The three Mojo runtime libraries total
   1.96 MB.
3. **Static linking is impossible**: only `.so`, no `.a`. Additionally all three
   runtime libraries link `libstdc++.so.6` themselves, so a static libstdc++
   would change nothing.
4. `mojo build` needs a C compiler driver on the host at build time.
5. `mojo precompile` emits `.mojoc` **without link metadata**; a raylib binding
   cannot ship its linker flags.
6. The official answer to this is conda/pixi. bojo replaces that path.

A single-file binary is possible as a **wrapper** (native stub, unpacks at start),
not as static linking: 26.4 MB / 10 ms, stripped 5.29 MB / 4–5 ms (§18).

## 4. Terms

| Term | Meaning |
|---|---|
| **repo** | git repository, identified by normalised URL + commit |
| **pkg** | package in a repo, declared in `bojo.pkg.toml` |
| **cmd** | pkg with `kind = "cmd"`, produces a binary |
| **route** | import path a pkg provides |
| **cache** | **project-local** working tree under `.bojo/cache/` (D32) |
| **artifact** | `.mojoc` for (repo, commit, pkg, toolchain, dep closure) |
| **hook** | external process a pkg wants to run during a build |
| **allow** | an explicit approval of a hook, one line in `bojo.allow` |

## 5. Files

### 5.1 `bojo.toml` — what the project needs

```toml
[project]
name    = "example"
version = "0.1.0"
entry   = "main.mojo"

[version]
mojo   = "1.2.0"                   # selects the toolchain (D43)
python = "3.12"                    # optional; presence enables bundling (D46)

[build]
output = "dir"                     # dir | onefile
strip  = true

[deps.local_lib]                   # local, no ".."
path = "lib/local_lib"

[[deps-from]]                      # one table per (repo, version)
repo  = "https://github.com/LukasLow/example_pkg"
tag   = "addition/v0.0.1"
libs  = ["addition"]
alias = { addition = "sum" }        # only on a name collision

[deps.python]                      # Python packages, wheels only (D48)
numpy  = "2.1.0"
orjson = "*"

[hooks]                            # own hooks, no allow needed (D12)
pre-build  = [["templ", "generate"]]
post-build = []
```

### 5.2 `bojo.pkg.toml` — one package

Repo root (the only entry point):

```toml
[repo]
url     = "https://github.com/LukasLow/example_pkg"
license = "Apache-2.0"

import = ["lib/addition", "cmd/hello"]
```

Package (`lib/addition/bojo.pkg.toml`). Every package — library **and** command —
declares the versions it was built against; there are no min/max ranges (D47):

```toml
[pkg.addition]
modules = ["addition"]

[pkg.addition.version]
mojo   = "1.2.0"
python = "3.12"                    # only if this package uses Python interop

[pkg.addition.deps]
prim_endian = { in-repo = "prim_endian" }

[[pkg.addition.deps-from]]
repo = "https://codeberg.org/lukas/mojo-tls"
tag  = "tls/v0.2.0"
libs = ["tls"]

[pkg.addition.linker]
libs    = ["m"]
libdirs = []
runtime = []

[pkg.addition.hooks]
pre-build  = [["gen", "--in", "lib/addition"]]
post-build = []
```

Command (`cmd/hello/bojo.pkg.toml`):

```toml
[pkg.hello]
kind  = "cmd"
entry = "main.mojo"

[pkg.hello.deps]
addition = { in-repo = "addition" }
```

Rules:

- `import` holds **directory paths**, relative, no `..`, no file name (D5).
  The graph is transitive; a cycle is an error; a file diamond is deduplicated
  before the name check (D5b).
- `[repo]` exactly once per tree. The same pkg name in two files is an error.
- Several `[pkg.*]` per file are allowed; one file per pkg is recommended.

### 5.3 `bojo.lock` — the truth (committed)

```toml
version = 1
mojo    = "1.2.0-2016d0cd"          # fingerprint from `mojo --version`

[[package]]
name   = "addition"
url    = "https://github.com/LukasLow/example_pkg"
tag    = "addition/v0.0.1"
commit = "9f3a2b1c4d5e6f70a1b2c3d4e5f60718293a4b5c"
tree   = "git-tree:4d1e…"           # git tree OID of the commit
alias  = "sum"                      # optional

[python]                            # only when the project bundles Python
version = "3.12"

[[python.package]]
name    = "numpy"
version = "2.1.0"
sha256  = "…"                       # hash of the installed wheel (D48)
```

The wheel hash lives in the lock and **not** in `bojo.allow`: it is an install
artefact, not a trust decision. If the same version suddenly resolves to a
different hash, the build fails — that is the supply-chain alarm.

### 5.4 `bojo.allow` — trust log (JSONL, committed)

```jsonl
{"repo":"https://github.com/LukasLow/example_pkg","commit":"9f3a2b1c4d5e6f70a1b2c3d4e5f60718293a4b5c","pkg":"web_templ","hook":"pre-build","command":["templ","generate"],"binary":"/usr/local/bin/templ"}
```

Append-only, written only by `bojo allow`. One entry per line.

## 6. Versions and tags

| Rule | Value |
|---|---|
| Tag format | `<pkg>/v<major>.<minor>.<patch>[-<pre>]` |
| Slashes | exactly one |
| Truth | the **commit**; the tag is a label |
| `@latest` | highest semver under `<pkg>/*` |
| `@<hash>` | 7–40 hex, must resolve **unambiguously**, otherwise error |
| Tag without `pkg/` | ignored, with a warning |

## 7. Repository identity and URLs

```
https://github.com/LukasLow/example_pkg#addition@v0.1.0
└───────────── repo ──────────────┘└─ pkg ─┘└ version ┘
```

Normalisation, in this order (D14a):

1. Parse into components (scheme, host, path) — never string surgery.
2. SSH/scp forms (`git@host:owner/repo`, `ssh://git@host[:22]/…`) → `https://`.
3. **Strip userinfo** and discard it; URLs with credentials are rejected (D25).
4. Remove query and fragment; strip trailing `/`, doubled `//` in the path, and
   `.git`/`.GIT`.
5. Lowercase scheme and host; **path stays case-sensitive**; IPv6 in `[…]`.
6. Remove default ports.

`urlhash12 = hex(sha256(normalised))[:12]`.

Without `#`: the whole repo, pkg name = repo name, otherwise a recursive scan
(depth ≤ 4). Fallback, not the main path (D4).

## 8. Cache — project-local (D32)

```
project/
  .bojo/
    cache/
      src/<urlhash12>/<commit12>/            working copy, writable
          meta.json                          {url, tag, commit, tree, fetched}
          …repo content (hooks may write here)…
      obj/<urlhash12>/<commit12>/<pkgslug>/<toolchain>/<deps12>/<route>.mojoc
    modules/<route> -> cache/src/<…>/<module root>
    mojo-cache/                              MODULAR_CACHE_DIR
    state.json                               resolved objects, fingerprints
  out/                                       result of `bojo build`
```

- **Integrity is checked while fetching**, not forever: clone the pinned commit,
  compare the tree OID with `bojo.lock`. Afterwards the tree is intentionally
  writable — hooks and builds write there (D33).
- `<toolchain>` = fingerprint from `mojo --version` (`1.2.0-2016d0cd`), not the
  display name (D35).
- `<deps12>` = 12 hex over the **canonically sorted** list of
  `<urlhash12>@<commit12>` of the transitive dependencies (D23).
- `<pkgslug>` = pkg name lowercased, `[a-z0-9-]`; a collision is a conflict.
- `<route>` is the import name **and the file name of the `.mojoc`** — per
  `mojo precompile`, the file name determines the import name. An alias
  therefore produces a second artifact of the same source (D8/D37).

`.bojo/` is gitignored. `bojo clean` removes it.

Cost of this model, accepted on purpose (D32): every dependency exists once per
project, not once per machine; no shared build cache; `install`/`run` cache
nothing at all (§11a).

## 9. Resolution

| # | Rule |
|---|---|
| R1 | **One route → exactly one pkg.** Checked on the final (post-alias) route name, case-folded, and against project-local top-level directories. |
| R2 | `alias` only for a pkg with **exactly one** route that starts with the pkg name; the alias replaces the first segment. |
| R3 | `in-repo` deps inherit the commit of the repo. |
| R4 | A cycle in the pkg graph is an error with a path. |
| R5 | Two pkg names colliding inside one repo tree are an error. |
| R6 | Determinism: conflicts are errors, never overwrites; import order is irrelevant. |
| R7 | `alias` is allowed; it produces a second `.mojoc` under the route name, because the file name determines the import name (D8/D37). |

## 10. Module root and `-I`

```
project/.bojo/modules/
  sum      ->  cache/src/<hash>/<commit12>/lib/addition
  web_http ->  cache/src/<hash>/<commit12>/lib/web_http
```

- One `-I` on `.bojo/modules`, plus the project root for project-local packages
  (D27).
- The module root is swapped via a pointer symlink plus rename, or a temp dir plus
  `rename` where the platform allows an atomic directory exchange (D26a).
- The consumer's Mojo source stays unchanged: `from sum import add` or
  `from addition import add`, depending on the alias. No enforced `akku.` prefix
  (D13/D14).

## 11. Build pipeline

| # | Step | Failure |
|---|---|---|
| 1 | Load and validate `bojo.toml` | exit 1 |
| 2 | Resolve; fetch missing repos into `.bojo/cache/src`, verify the tree OID | exit 4 |
| 3 | `--locked`: compare lock against resolution | exit 5 |
| 4 | Take the project lock; swap `.bojo/modules` | exit 1 |
| 5 | Check hooks (§12). Missing approval → **abort, execute nothing** | exit 2 |
| 6 | Run pre-build hooks (topological, `cwd` = package dir, may write) | exit 2/3 |
| 7 | Precompile into `cache/obj` — **not** for pkgs with a `pre-build` hook (D40) | exit 3 |
| 8 | Collect linker flags from every pkg (`[linker]`) | — |
| 9 | `mojo build <entry> -I .bojo/modules -I . <flags>` with the toolchain from `[version] mojo` (D43/D47) | exit 3 |
| 10 | Post-build: `strip`, closure into `out/lib`, relative RPATH at link time, re-sign on macOS | exit 3 |

`MODULAR_CACHE_DIR` = `.bojo/mojo-cache` (D15). Step 10 per platform:

| | Linux | macOS |
|---|---|---|
| Closure | `ldd` | `otool -L` |
| Path | `-Xlinker -rpath -Xlinker '$ORIGIN/lib'` | `install_name_tool -change … @loader_path/lib/…` |
| After modifying the Mach-O | — | `codesign -s - --force` |

### 11a. `bojo run` and `bojo install` — ephemeral (D34a)

Both build **outside** the project cache, in a temporary directory
(`os.MkdirTemp`, e.g. under `$TMPDIR`):

```
tmp/bojo-<rand>/
  src/<commit12>/      clone at the pinned commit
  obj/                 precompile
  out/<cmd>            binary
```

Flow: resolve → clone (verify tree) → hook gate → build → for `install` copy the
binary to `~/.bojo/bin/<name>`, for `run` execute it → remove the temp directory.

- No cache, no reuse: every `install`/`run` compiles from scratch (D34a).
- `$BOJO_BIN` overrides `~/.bojo/bin`.
- If the temp filesystem is `noexec`, the run aborts with a clear message;
  `--build-dir <path>` allows an exec-capable location.
- A name collision in `~/.bojo/bin` is an error; `--force` overwrites (D30).

## 12. Hooks and trust

### 12.1 Shape

```toml
[pkg.web_templ.hooks]
pre-build  = [["templ", "generate"]]
post-build = [["cargo", "build", "--release"]]
```

- Array of argv arrays. No shell, no file (D16a). `cwd` = package directory,
  `BOJO_*` set, exit ≠ 0 aborts.
- Hooks **may** write into the package directory — that is intended (D33). The
  integrity guarantee applies at fetch time, not to the working tree.

### 12.2 Approval

No built-in trust (D16). `cargo`, `cmake`, `meson`, `make` are treated exactly
like any other command.

```
blocked: pkg 'web_templ' wants to run a hook before build
  repo     https://github.com/LukasLow/example_pkg
  commit   9f3a2b1c4d5e
  pkg      web_templ
  hook     pre-build
  command  templ generate
  binary   /usr/local/bin/templ

Not run. Inspect the repo and the command above, then approve with:

  bojo allow 3c1d9e

Blocked by 1 hook. Nothing was executed.
```

- ID = hash over (repo, commit, pkg, hook, command array), must be unambiguous.
- `bojo allow --json '{…}'` for agents; `bojo allow --list` shows approvals and
  marks stale ones.
- **No `-y`, no "allow all", no Enter-to-confirm.**
- A line is only valid if (repo, commit) is in the current lock (D28).
- Own hooks (`[hooks]` in the project's own `bojo.toml`) need no approval but are
  printed on every build (D12).
- Path deps have no commit: directory hash, recomputed on every build; symlink
  targets are hashed, not followed (D17).

### 12.3 Residual risks

- **No sandbox in v0.1.** The only protection is the explicit approval.
- **Hook chains are invisible:** `["cargo","build"]` runs `build.rs`,
  `["cmake",…]` runs CMake code. The allow line shows only the outer command
  (D29).
- **Committed allow lines can pre-approve.** Mitigations: binding to the lock
  (D28), one log line per executed foreign hook, `--strict-hooks`.
- **`PATH` changes:** the command string is approved, not the resolved binary.
  `bojo allow` records the resolved path in the line (D39).

## 13. CLI

| Command | Purpose |
|---|---|
| `bojo init` / `bojo new` | create a project |
| `bojo deps list` | show Mojo/Python versions, repositories and Python packages (D50) |
| `bojo deps add <url>#<pkg>@<ver> [--as <name>]` | add a repository dependency |
| `bojo deps add-python <name>[@<ver>]` | add a Python package to `[deps.python]` |
| `bojo deps check` | newer versions and CVEs (POST-V0.1, P21) |
| `bojo remove <pkg>` / `bojo update [pkg]` | remove / re-resolve |
| `bojo build [--locked]` | build into `out/` |
| `bojo run <url>#<pkg>@<ver>` | ephemeral build and run (§11a) |
| `bojo install <url>#<pkg>@<ver>` | ephemeral build, binary to `~/.bojo/bin` (§11a) |
| `bojo test` / `bojo fmt` / `bojo doc` | delegate to the driver |
| `bojo tree` / `bojo why <pkg>` | graph / provenance |
| `bojo verify` | check the lock against a fresh checkout (never the working tree) |
| `bojo doctor` | mojo version + fingerprint, C compiler, triple, PATH, `~/.bojo/bin` |
| `bojo toolchain mojo install <ver>` | install a Mojo toolchain into `~/.bojo/mojo/<ver>` (D43) |
| `bojo toolchain mojo list` | list installed Mojo toolchains |
| `bojo toolchain python install <ver>` | install a relocatable CPython into `~/.bojo/python/<ver>` (D44) |
| `bojo toolchain python list` | list installed Python runtimes |
| `bojo toolchain remove <mojo\|python> <ver>` | delete an installed toolchain |
| `bojo cache list` / `bojo cache clear` | show / clear the project cache |
| `bojo allow <id>\|--json\|--list` | approve / show hooks |
| `bojo clean [--all]` | remove `.bojo/` (`--all`: also `out/`) |
| `bojo skill [<runtime>]` | print the agent skill as markdown (§19) |

Exit codes:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | usage or configuration error |
| 2 | blocked by a missing hook approval — nothing was executed |
| 3 | build or link failure |
| 4 | network, clone or checkout failure |
| 5 | lock conflict (`--locked`) |

## 14. Output modes and platforms

| Mode | Description | Size / start (Linux, measured) |
|---|---|---|
| `dir` (default) | `out/` with binary + `lib/`, relative RPATH | 5.29 MB / 4–5 ms |
| `onefile` | one self-extracting file (C stub + `app.bin` + `lib/*`) | measured: 4.28 MB for the hello-world demo, start ~5 ms after first extraction |
| `static` | does not exist (D18) | — |

| | Linux | macOS | Windows |
|---|---|---|---|
| Libs | `.so` | `.dylib` | `.dll` |
| Relative path | `$ORIGIN/lib` | `@loader_path/lib` | next to the `.exe` |
| Stub finds itself | `/proc/self/exe` | `_NSGetExecutablePath` | `GetModuleFileNameW` |

- v0.1: macOS (primary) and Linux (CI). Windows not (D19).
- The stub **execs** the extracted binary; extracted libraries are re-signed ad
  hoc when needed (D31). Extraction goes to `os.UserCacheDir()/bojo/run/<hash>/`,
  not `/tmp` (D21).
- **Signing is not bojo's job** (D20). Distribution is the app author's
  responsibility; bojo only reports.

## 15. Output conventions

- One fact per line, no spinners, no mandatory colour.
- Errors are complete sentences and name the next command.
- `--json` for every data-producing command.
- Progress only for network and build, as lines.

## 16. Decisions

| # | Decision | Rejected alternative | Reason |
|---|---|---|---|
| D1 | Go | Rust, Mojo | `os.UserCacheDir`, `os/exec` argv semantics, static binary; **the resolver is built by hand** |
| D2 | TOML | YAML | indentation is not semantics, explicit types, Cargo/uv parity |
| D3 | `bojo.toml`, `bojo.pkg.toml`, `bojo.lock`, `bojo.allow` | `bojo.yml` + `bojo_libs.yml` | clear roles |
| D4 | `#` separates repo and pkg | guessing paths, an index file | depth stays arbitrary |
| D5 | `import` uses directory paths, fixed file name | file paths, separate repo document | one file per directory |
| D5a | import graph is transitive, conflicts are errors | "last one wins" | determinism |
| D5b | file diamonds are allowed (dedupe before the name check) | diamond is an error | legal structure |
| D6 | tag `pkg/vX.Y.Z` | `vX.Y.Z` per repo | the name is in the manifest |
| D7 | `alias` inside the dep block | global `[alias]` table | with two same-named deps it is otherwise ambiguous |
| D8 | every pkg is precompiled; the **`.mojoc` file name** is the import name | a `precompiled` flag, min/max hardening | `mojo precompile` documents the file name as the import name |
| D9 | **removed** — the cache is project-local (D32) | `os.UserCacheDir` | see D32 |
| D10 | `cache/src/<urlhash12>/<commit12>` | `src/<host>/<owner>/<repo>` | depth, case, special characters |
| D11 | one version per route | Cargo-style coexistence | Mojo imports are paths |
| D12 | own hooks without approval, but printed | lock your own hooks too | the user wrote that file |
| D13 | import route is free (`modules`) | enforce an `akku.` prefix | the consumer imports what they chose |
| D13a | **removed** — no tag in the path (D32) | `tagslug` | the tag is a label, the commit is the identity |
| D14 | the `akku.` prefix is gone | enforce it | see D13 |
| D14a | URLs are parsed and normalised per component | sequential string replacement | `git@` would otherwise eat the scp case |
| D15 | `MODULAR_CACHE_DIR` per project | global | otherwise `~/.modular` grows unbounded |
| D16 | no built-in trust for any build system | privilege cargo/cmake | foreign code brings its own hooks |
| D16a | hooks are argv arrays, no shell | shell string, `.sh` | cross-platform, no quoting |
| D17 | path deps: directory hash, recomputed per build, symlink targets hashed | store the hash once | TOCTOU |
| D18 | no `static` mode | "later" | upstream #1317 is open |
| D19 | v0.1 targets macOS + Linux | Linux first | the development machine is macOS |
| D20 | no signing by bojo | notarise | `run`/`install` build locally |
| D21 | onefile unpacks to `UserCacheDir` | `/tmp` | `/tmp` is cleaned at runtime too |
| D22 | no per-file cache granularity | hash every file | milliseconds against undebuggability |
| D23 | the `obj` key includes the dep closure, canonically sorted | own commit only | stale artifacts after a dep update |
| D24 | no `flags12` dimension | flags in the key | `precompile` has no `-O/-g/-D` |
| D25 | never store credentials | pass userinfo through | secret leak |
| D26 | every cache write is atomic (temp + `rename`) | write in place | an aborted run poisons the cache |
| D26a | module root via pointer symlink and swap | directory `rename` | directory `rename` is not atomic |
| D27 | a second `-I` on the project root | `.bojo/modules` only | project-local packages stay importable |
| D28 | an allow line is only valid if the commit is in the lock | the line always counts | pre-approval via pull request |
| D29 | hook chains are a documented residual risk | pretend to sandbox | v0.1 cannot confine processes |
| D30 | `install` aborts on a name collision | silently overwrite | traceability |
| D31 | the stub **execs**, does not `dlopen` | `dlopen` | `@loader_path` is relative to the binary |
| D32 | **cache is project-local under `.bojo/cache/`** | global object cache | no GC, no reference registry, no cross-project races; cost: N copies |
| D33 | **hooks and builds may write into the working tree** | read-only plus overlay | wanted; integrity is a fetch-time property |
| D34 | core stays Mojo-only, `Driver` is an internal Go struct | public driver API | an interface without a second implementer is an untested promise |
| D34a | `run`/`install` build ephemerally in `os.MkdirTemp` | own cache | saves cache format, GC and state |
| D35 | toolchain identity = fingerprint from `mojo --version` | display name `1.2.0` | two builds of the same version are incompatible |
| D36 | `bojo verify` checks against a fresh checkout | check the working tree | the tree is mutated by hooks |
| D37 | `alias` produces a second `.mojoc`; route is part of the `obj` key | forbid alias | keeps alias available for name collisions |
| D38 | `triple` is not part of the `.mojoc` key | triple in the key | `.mojoc` is architecture-independent |
| D39 | the allow line records the resolved binary path | command string only | makes `PATH` changes visible |
| D40 | pkgs with a `pre-build` hook are **not** artifact-cached | hash the hook output | foreign tool versions would create stale `.mojoc` |
| D41 | `bojo skill` prints the skill as markdown on stdout | a separate `bojo docs` command | `bojo skill > …/SKILL.md` is one step, not a build |
| D42 | `[[deps-from]]` is an array of tables with a `repo` field | `[deps-from."<url>"]` | tags are per pkg; one table per repo allows only one version |
| **D43** | toolchains live machine-wide in `~/.bojo/mojo/<ver>/` and `~/.bojo/python/<ver>/`, installed via `bojo toolchain install <family> <ver>`; `[version] mojo` selects one | per-project toolchain, manual install | Mojo ships as a glibc-only Python package; a venv pins it per version and a build needs no Python |
| **D44** | bundling uses a relocatable CPython (`python-build-standalone`) plus wheels via `pip --target` | ship a conda/pixi env | conda envs bake absolute prefixes into scripts, `.pth` files and native RPATHs; relocation is guesswork |
| **D45** | a launcher stub sets `PYTHONHOME`/`PYTHONPATH` and execs the payload (`app.bin`) | require the user to set the variables | a binary cannot set its own environment; `dir` and `onefile` share one mechanism |
| **D46** | `[python]` is opt-in; without it the target must provide Python 3.10–3.14 | always bundle | a Python-capable bundle is roughly ten times larger; pure Mojo programs stay at 5 MB |
| **D47** | one `[version]` block (`mojo`, optional `python`) in `bojo.toml` and in every package; **no min/max ranges** | `min`/`max` compatibility windows | a range is a guess about the future; one declared version is a fact |
| **D48** | Python packages are wheels only; `.pth` files are scanned; an sdist needs an explicit approval | plain `pip install` | wheels can still execute code at start-up via `.pth`, and sdists run a build backend |
| **D49** | toolchains are namespaced: `bojo toolchain mojo …`, `bojo toolchain python …` | one flat `toolchain install` | two runtimes with independent versions and stores |
| **D50** | dependencies live under `bojo deps …` (`list`, `add`, `add-python`, `check`) | a top-level `get` | one namespace for everything that changes the manifest |
| **D51** | the launcher sets `MOJO_PYTHON_LIBRARY` (absolute `libpython` path) in addition to `PYTHONHOME`/`PYTHONPATH` | rely on `PATH` discovery | research shows `MOJO_PYTHON_LIBRARY` is the primary lookup; a wrong `LIBDIR` ends in `dlopen("")` |
| **D52** | the runtime closure is copied to `out/lib`, and the relative RPATH (`$ORIGIN/lib`, `@loader_path/lib`) is passed **at link time** | rewrite the RPATH afterwards | Mojo only passes `-Xlinker`; a relative entry can be appended, a `patchelf` step would be an extra dependency |
| **D53** | `onefile` compiles a **C stub at build time** with the host `cc` and appends `app.bin` + `lib/*` plus a 24-byte trailer | embed a prebuilt stub, or require Go at runtime | Mojo already needs a C driver, so no new dependency; the stub sets `LD_LIBRARY_PATH`/`DYLD_LIBRARY_PATH` and execs |

## 21. Runtime requirements (verified)

Research summary, sources in the commit message. What a `mojo build` artifact
needs on the target:

1. **Always** `libKGENCompilerRTShared.so` (plus `libMSupportGlobals.so`,
   `libAsyncRTRuntimeGlobals.so`, `libstdc++`, `libgcc_s`). The compiler links the
   runtime into every binary, including `def main(): pass`.
2. The loader finds them through an **absolute RUNPATH** into the build
   environment. `MODULAR_*` variables are compiler settings and are never read by
   `ld.so`; only `MODULAR_NVPTX_*` matters at runtime for GPU.
3. The target does **not** need the toolchain, but it needs the runtime libraries
   of the matching Mojo version.
4. **Python is `dlopen`ed, not linked.** Lookup order: `MOJO_PYTHON_LIBRARY`, then
   the first Python on `PATH` and its `LIBDIR`. A wrong `LIBDIR` ends in
   `dlopen("")` and a confusing abort inside the KGEN runtime.
5. Supported Python: **3.10–3.14** (3.15 fails, conda-forge split `libpython`).
6. GPU is `dlopen`-only (`libcuda.so.1`, `libamdhip64.so`, Metal); PTX is
   assembled at runtime.
7. macOS: `LC_RPATH` + `@rpath/…`, `install_name_tool`, then `codesign -s -`;
   `MACOSX_DEPLOYMENT_TARGET` decides the minimum OS in `LC_BUILD_VERSION`.
8. Measured glibc floor for the produced binary: 2.35.

Consequences already reflected in the spec: relative RPATH in step 10, the
launcher stub (D45/D51), and the `[python]` bundling rules (§20).

## 17. Open questions

1. Sandbox for hooks: own concept or platform facilities? → P5.
2. Does v0.1 need `bojo toolchain`? Without pinning the `obj` cache is worthless
   when the toolchain changes. → P1.
3. `bojo test` vs. an existing `Taskfile.yml`: respect, generate, replace? → P9.
4. Keep `akku` as a meta package? → P10.
5. Shared cache as an opt-in (`--shared-cache`) later? → P17.

## 18. Measurement log

Debian bookworm, aarch64, Mojo 1.2.0 (2016d0cd), gcc 12.

| Measurement | Value |
|---|---|
| `hello.mojo` via `mojo build` | 72,512 B, PIE, aarch64 ELF |
| NEEDED of the app | `libKGENCompilerRTShared.so`, `libc.so.6` |
| RUNPATH | `/root/.pixi/envs/mojo/lib` (absolute) |
| transitive | `libMSupportGlobals.so`, `libAsyncRTRuntimeGlobals.so`, `libstdc++.so.6`, `libgcc_s.so.1`, `libm`, `libdl` |
| Mojo runtime total | 1,239,944 + 53,528 + 669,224 = 1.96 MB |
| `libstdc++.so.6` | 23.6 MB → `strip --strip-unneeded` 2,698,712 B |
| single file, complete | 26,433,696 B, start 10 ms |
| single file, stripped | 5.29 MB, start 4–5 ms |
| bare binary | 0.07 MB, start 2 ms |
| `-Xlinker -static` | fails (`cannot find -lgcc_s`) |
| `-Xlinker --static-libstdc++` | `ld: unable to disambiguate` |
| `libstdc++.a` in the env | absent |
| NEEDED of the runtime libs | each `libstdc++.so.6` + `libgcc_s.so.1` |
| `mojo build` without `cc` | `unable to find suitable c compiler for linking` |
| `-Xlinker -rpath -Xlinker '$ORIGIN/lib'` | RUNPATH is appended |
| stub with `LD_DEBUG=libs` | loads `libKGENCompilerRTShared.so` from the extraction directory |
| `mojo build --help-hidden` | contains `-strip-file-prefix <PATH>` (confirmed) |

Upstream: `modular/modular#1317`, open since 2023-11-21; the follow-up question
from 2026-09-14 is unanswered.

## 20. Python interop and bundling

Mojo has first-class Python interop, but neither the interpreter nor any Python
package is embedded: `mojo build --help` states that Python libraries used by a
project "are not included in the executable binary, so they must be provided by
the environment where you run the executable". A single-file artifact therefore
has to carry a Python runtime of its own or the target machine must provide one.

### 20.1 Two modes

| Mode | When | Consequence |
|---|---|---|
| **host Python** | `[python]` absent | interop works only where Python 3.10–3.14 (plus the used packages) is installed; `bojo doctor` names the exact requirement |
| **bundled Python** | `[python]` present | interpreter and packages ship with the artifact; no Python needed on the target |

```toml
[version]
python = "3.12"

[deps.python]
numpy  = "2.1.0"
orjson = "*"
```

### 20.2 Why a relocatable CPython, not conda/pixi

A conda/pixi environment is **not relocatable**: prefixes are baked into
`pyvenv.cfg`, shebangs, `.pth` files and RPATHs of native extensions. Shipping one
means shipping a path-rewriting tool and hoping every package tolerates it.

bojo therefore bundles a **python-build-standalone** interpreter (relocatable by
design, one tarball per triple) and installs wheels into it with
`pip install --target`. Wheels of native packages (numpy, orjson, …) are plain
`.so` plus Python files and survive relocation as long as the ABI matches the
bundled interpreter — which it does, because the interpreter *is* the one used to
install them.

### 20.3 Layout

`output = "dir"` with `[python]`:

```
out/
  app            launcher stub: sets PYTHONHOME / PYTHONPATH, then execs app.bin
  app.bin        the Mojo binary
  lib/           Mojo runtime libraries (relative RPATH)
  python/        relocatable interpreter + site-packages
```

`output = "onefile"`: the same four pieces are appended to one stub, which
extracts them to `<cachedir>/bojo/run/<hash>/` and execs `app.bin` (D45).

### 20.4 Sizes (to be measured, not yet)

| Piece | Expectation |
|---|---|
| CPython standalone, stripped | ~25–35 MB compressed, ~60–80 MB unpacked |
| numpy wheel | ~20 MB unpacked |
| Mojo runtime + app | ~5.3 MB (measured, §18) |

A Python-capable `onefile` therefore lands in the **40–70 MB** range. This is the
honest price for "no Python on the target"; the numbers are estimates until the
first real measurement is in §18.

### 20.5 Rules

- The bundled interpreter version must match what the Mojo runtime expects; a
  mismatch is a hard error, not a warning (D44).
- `bojo build` warns when Mojo source imports `python`/`PythonObject` but
  `[version] python` is absent, and names the target requirement.
- `bojo doctor` reports: interpreter found, version, `libpython` path, whether
  the current project bundles one.
- Native wheels are copied as-is; no compiler is required on the target.
- Nothing about conda/pixi is used at runtime. `bojo bundle --python-env <path>`
  (best effort, path rewriting) is a POST-V0.1 escape hatch, not the default.

### 20.6 Wheels only, and what a wheel can still do

A wheel is installed, not executed — but two things can run code anyway:

1. **`.pth` files** are imported by `site` at interpreter start-up. They are the
   standard trick to execute code in every Python process of an environment.
2. **sdists** run an arbitrary build backend during `pip install` (equivalent to
   a hook, and therefore subject to the same trust rules).

Rules (D48):

- `pip install --only-binary=:all:` — an sdist is refused. A package that only
  ships an sdist needs an explicit approval line, the same shape as a hook.
- After installing, bojo scans every `.pth` file in the environment. A line that
  is not a plain path (i.e. `import …` / `exec(…)`) blocks the build and prints
  the file, the line and the package that owns it.
- The scan result is recorded in the lock (`python = "3.12"` plus a hash of the
  resolved package set), so a later build does not silently pick different
  wheels.

## 19. Agent skill (`bojo skill`)

bojo is mostly used by agents, so the CLI ships its own skill instead of
maintaining a second copy of the documentation.

```
bojo skill > .agents/skills/bojo/SKILL.md
```

- **Source of truth** is `skill/bojo.md` in the repository, embedded via
  `go:embed` (`skill/skill.go`). One file, two output paths.
- Output is plain Markdown on **stdout**, no colour, no progress, no epilogue.
  Exit 0, no network.
- `bojo skill` (no argument) is the full version. Runtime hint:
  `bojo skill claude`, `bojo skill codex`, `bojo skill opencode` — in v0.1 all
  three emit **identical output**; the arguments are reserved so runtime-specific
  notes can be added later without changing the command line (D41).
- The text may only describe what is implemented. If the CLI diverges, the skill
  is wrong, not the user.
- It states the limits explicitly (no static ELF, no embedded runtime, no
  automatic hooks) and the exit-code semantics, because agents act on them.
