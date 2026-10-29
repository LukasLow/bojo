# bojo — after v0.1

Everything deliberately kept out of v0.1. Each item names: what, why not now, the
precondition, and a rough size (S / M / L). Order = recommended priority.

---

## P1 — Toolchain management

**Status: done in v0.1** (`bojo toolchain list`, `bojo toolchain install
<mojo|python> <version>`, `bojo toolchain mojo v1.2`). Kept here only as a
pointer: multi-version *switching* per project is already covered by
`[version] mojo`.

---

## P2 — Extraction cache for `onefile`

**Status: done.** Unpacked once into `<cachedir>/bojo/run/<content-hash>/`;
later starts `exec` straight there. Startup time is *not* a work item: a CLI
that starts in a few milliseconds is fast enough, and every further
optimisation (page-cache tricks, memfd, in-place mapping) buys milliseconds at
the price of a second code path.

**What.** Unpack the embedded libraries once into
`<cachedir>/bojo/run/<content-hash>/`; later starts `exec` straight there.

**Why not now.** The overhead is 2–3 ms (measured: 2 ms bare, 4–5 ms stripped,
10 ms unstripped). Irrelevant for a CLI.

**Precondition.** Cleanup strategy and an integrity check so a half-extracted
directory is never used.

**Size.** S

---

## P3 — Version ranges

**What.** `^1.2`, `~1.2.3`, `>=1.2,<2` instead of exact versions plus `latest`.

**Why not now.** Exact plus `latest` covers the need; ranges require a semver
implementation and a resolution strategy.

**Precondition.** A clear conflict rule that fits "one version per route" (D11).

**Size.** M

---

## P4 — `--offline` and cache transport

**What.** `bojo build --offline` forbids network access; `bojo cache export|import`
transports the project-local cache as an archive (air-gapped networks, build
farms).

**Why not now.** The cache is project-local since D32, so the most important
vendor function is built in. What remains is transport between machines.

**Precondition.** Version the archive format; carry `bojo.lock`.

**Size.** S

---

## P5 — Sandbox for hooks

**What.** Run hooks confined: write access only to the project and the cache,
optionally no network. Platform facilities: `sandbox-exec` (macOS), `bwrap`
(Linux), Job Objects (Windows).

**Why not now.** v0.1 protects solely through explicit approval (D12/D16). A
half-finished sandbox is worse than none because it implies safety.

**Precondition.** Document the threat model; define behaviour when the platform
facility is missing.

**Size.** L

---

## P6 — `bojo licenses`

**What.** License report across all dependencies; **warn** when an LGPL/GPL
library would be embedded into a `onefile` bundle.

**Why not now.** Unnecessary for personal use, important for distribution.

**Precondition.** `license` already exists in `[repo]` (§5.2); only the transitive
evaluation is missing.

**Size.** S

---

## P7 — Windows

**What.** `.dll`, directory next to the `.exe`, `GetModuleFileNameW`.

**Why not now.** Mojo does not support Windows natively (D19). No pretended
support.

**Precondition.** Mojo support for Windows.

**Size.** M (once Mojo is there)

---

## P8 — GPU targets in the cache

**What.** `--target-accelerator` as an additional key dimension so CPU and GPU
builds do not collide.

**Why not now.** v0.1 builds only the host configuration.

**Precondition.** Clarify whether a pkg must ship several targets at once.

**Size.** S

---

## P9 — Relation to `Taskfile.yml`

**What.** Decide whether `bojo test`/`bojo fmt` respect an existing Taskfile,
generate one, or replace it.

**Why not now.** Two parallel build systems in one repository confuse people;
that must be a deliberate decision.

**Precondition.** Practical test against the existing `mojoakku` setup
(`task test`, `task ci::smart`).

**Size.** S–M

---

## P10 — `akku` as a meta package

**What.** A pkg `akku` that bundles the individual libraries and keeps offering
the old import paths `akku.net_socket`.

**Why not now.** v0.1 deliberately drops the prefix (D13); the meta package is a
migration aid.

**Precondition.** `modules` must be able to bundle several routes from different
pkgs.

**Size.** S

---

## P11 — Integrity and signing of dependencies

**What.** A checksum database or signatures for git dependencies (`GOSUMDB` /
minisign model), protection against a manipulated host.

**Why not now.** v0.1 pins commits and verifies the `tree` hash (`bojo verify`).
That is TOFU, but honestly labelled.

**Precondition.** Where does the public key come from (bootstrapping)?

**Size.** L

---

## P12 — Harden concurrent builds in one project

**What.** Project lock with holder PID, timeout and stale detection; object-level
locking inside `.bojo/cache`.

**Why not now.** v0.1 has a project lock (§11 step 4) and atomic writes (D26).
Because the cache is project-local (D32), cross-project locking is gone entirely.

**Precondition.** Mandatory before a public release.

**Size.** S

---

## P13 — Reproducible builds

**What.** `SOURCE_DATE_EPOCH`, deterministic paths (`-strip-file-prefix` exists),
byte-identical bundles.

**Why not now.** Only needed for verifiable distribution.

**Precondition.** Measure which paths and timestamps Mojo actually embeds.

**Size.** M

---

## P14 — `bojo self update` / `bojo self install`

**What.** Self-update, self-install into `~/.bojo/bin`.

**Why not now.** `go install` and a Nix flake are enough (D1).

**Size.** S

---

## P15 — Public documentation

**What.** README, command reference, "why not static?" page, migration guide from
`akku.` imports.

**Why not now.** Planning documents are internal.

**Size.** M

---

## P16 — Platform-specific stripping and path shortening

**What.** `strip` with the right flags per platform (GNU `--strip-unneeded` vs.
macOS `strip -x`), and a fallback if `-strip-file-prefix` behaves differently on
macOS. Result: comparable bundle sizes and reproducibility on both systems.

**Why not now.** The measurements (§18) are Linux; the macOS divergence is known
but unmeasured (review finding B24).

**Precondition.** Measure once on macOS: size before/after `strip`, behaviour of
`-strip-file-prefix`.

**Size.** S

---

## P17 — Shared cache as an opt-in

**What.** `bojo build --shared-cache` (or `BOJO_SHARED_CACHE=<path>`): a
machine-wide object and artifact cache in addition to the project-local one, for
CI matrices and many projects on one machine. Includes ref counting and GC.

**Why not now.** D32 chooses the project-local cache on purpose: no reference
registry, no cross-project GC, no races, no shared-state format. A shared cache
brings all of that back and only pays off with measured benefit.

**Precondition.** Measurement: how much time and space does a shared cache
actually save?

**Size.** M

---

## P18 — `bojo test`

**What.** Discover `_tests/*.mojo` and run them through the selected toolchain,
with one line per file, a summary, and exit codes that match §13 (0 = pass,
3 = failing test). `--filter <pattern>` and `--json` for CI.

**Why not now.** v0.1 must first build reliably; a test runner is a thin layer on
top of the same pipeline and only becomes useful once builds are stable.

**Precondition.** Decide how it relates to an existing `Taskfile.yml` (P9).

**Size.** S

---

## P19 — Cache for bundled Python environments

**Status: done (project-local).** A prepared environment lives in
`.bojo/cache/python/<version>/<key>/` with `key` = hash of the sorted package
set (D55). A second build with the same set reuses it without touching the
network. A *machine-wide* cache stays open — see the precondition below.

**What.** Build the relocatable CPython + wheels once per (version, package set)
and reuse it, instead of re-provisioning for every project.

**Why not now.** Same reason as D32: no shared state until the single-project path
works. The package set is part of the key.

**Precondition.** A stable hash over `[python]` (version + resolved wheel set).

**Size.** M

---

## P20 — `bojo bundle --python-env <path>` (conda/pixi escape hatch)

**What.** Best-effort import of an existing conda/pixi environment into a bundle,
including prefix rewriting of scripts, `.pth` files and native RPATHs.

**Why not now.** Contradicts D44 (conda envs are structurally not relocatable) and
needs a rewriting tool of its own. Offer it only when someone actually depends on
it.

**Size.** L

---

## Explicitly **not** planned

### N1 — Per-file cache granularity

A cache object per single file. The benefit is milliseconds, the cost is a cache
nobody can debug. The seam stays open (`obj/` is content-addressed), but the
granularity is (commit, pkg, Mojo version, triple, flags).

**Re-evaluate when:** Mojo ships a stable, versioned artifact cache, or
measurements show a real bottleneck.

### N2 — An own registry or index server

Contradicts the decentralised model (git URLs plus `#pkg`). No need.

### N3 — Building static linking ourselves

Impossible until Modular ships a static runtime (`modular/modular#1317`). bojo
keeps the `output` switch open.
