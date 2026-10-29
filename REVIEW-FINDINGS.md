# Review findings and disposition

Source: independent reviews of the specification (`DESIGN.md`, `POST-V0.1.md`,
`AGENTS.md`) with an explicit mandate to hunt edge cases. Two rounds, 31 + 22
findings. Column **Disposition**: `fixed §x` = folded into `DESIGN.md` ·
`documented` = accepted residual risk · `open` = tracked in `POST-V0.1.md`.

## Round 1

| # | Severity | Finding | Disposition |
|---|---|---|---|
| B1 | high | Precompile ran **before** pre-build hooks; a hook that generates sources would cache an artifact built from incomplete source | **fixed §11**: hooks are step 6, precompile step 7 |
| B2 | high | `strip`/`install_name_tool`/stub break the signature on Apple silicon → the kernel kills the binary | **fixed §11** (table) and D20: after every Mach-O change `codesign -s - --force` |
| B3 | high | `obj` key without the dependency closure → stale `.mojoc` when a dep changes its commit | **fixed §8** (`<deps12>`), D23 |
| B4 | high | Cache writes not atomic → an aborted run leaves a broken artifact under a valid key | **fixed §8/§10/§11**, D26 |
| B5 | high | Path-dep directory hash stored instead of recomputed → TOCTOU, approval covers changed content | **fixed §12.2**, D17 |
| B6 | high | Credentials in the URL end up in the committed `bojo.lock` and in `meta.json` | **fixed §7**, D25: userinfo stripped, credentialed URLs rejected |
| B7 | high | A committed `bojo.allow` line can pre-approve foreign hooks (a PR adds dep and allow line together) | **partly fixed** §12.3 + D28 (line valid only for a commit in the lock, one log line per build, `--strict-hooks`); residual risk documented |
| B8 | medium | Alias collision: route `net_socket` → `socket` collides with a genuine `socket` from another repo; R1 only checked pre-alias | **fixed §9 R1**: check on the final, post-alias name |
| B9 | medium | Two pkgs of the **same** (repo, commit) providing the same route were not caught | **fixed §9 R1**: "one route → exactly one pkg" instead of per (repo, commit) |
| B10 | medium | Alias for a pkg with several routes was ambiguous | **fixed §9 R2**: alias only for pkgs with exactly one route |
| B11 | medium | Case-only routes pass the string comparison, collide on APFS | **fixed §9 R1**: comparison is case-folded |
| B12 | medium | Case collision in the `obj` path via the `<pkg>` segment | **fixed §8**: `<pkgslug>` lowercased; collision is a conflict |
| B13 | medium | §8 and §10 showed different `src/` path shapes | **fixed**: one form, `<commit12>` only |
| B14 | medium | URL normalisation incomplete (`HTTPS://`, `.GIT`, `?ref=`, `//`, `ssh://…:22`) → duplicate objects | **fixed §7**: seven steps incl. query, SSH forms, IPv6 |
| B15 | medium | A short commit hash could be ambiguous | **fixed §6**: must resolve uniquely, else error |
| B16 | medium | `--offline` was in the v0.1 CLI but deferred | **fixed**: removed from §13, stays P4 |
| B17 | medium | `AGENTS.md` referenced wrong sections | **fixed**: rewritten |
| B18 | medium | `precompiled = true` could not be enforced: no field for the exact compiler version | **fixed in round 3**: field removed; toolchain fingerprint in lock and cache key (D8/D35) |
| B19 | medium | `flags8` undefined and only 32 bits | **fixed §8**, D24: dimension removed |
| B20 | medium | `bin/` overwrote silently on a duplicate name | **fixed §13**, D30: abort, `--force` required |
| B21 | medium | Concurrent builds raced on `.bojo/modules` and `MODULAR_CACHE_DIR` | **fixed** §10/§11: project lock + atomic swap; largely moot after D32 |
| B22 | medium | Hook chains invisible: `cargo build` runs `build.rs`, `cmake` runs CMake code | **documented §12.3**, D29 |
| B23 | medium | `ldd` closure is Linux-only | **fixed §11**: `otool -L` + `install_name_tool` on macOS |
| B24 | low | `strip` and `-strip-file-prefix` are platform-specific | **open** → `POST-V0.1.md` P16 |
| B25 | low | `bojo allow <id>`: short-ID collision undefined | **fixed §12.2**: hash over all fields, must be unique |
| B26 | low | `tree` hash and what `bojo verify` compares were undefined | **fixed §5.3/§13**: git tree OID |
| B27 | low | Diamond import (same file via two paths) tripped the duplicate check | **fixed §5.2**: dedupe before the name check, D5b |
| B28 | low | The CLI table did not say that `run`/`install` go through the hook gate | **fixed §13** |
| B29 | low | `bojo cache verify` had no defined semantics | **fixed**: replaced by `bojo verify` against a fresh checkout (D36) |
| B30 | low | Stub: `exec` or `dlopen`? Determines RPATH and signing | **fixed §14**, D31: `exec` |
| B31 | low | A single `-I` blocks project-local packages | **fixed §10**, D27: second `-I` on the project root |

## Round 2 — architecture review

| # | Finding | Disposition |
|---|---|---|
| A1 | Hooks write into the "immutable" `src/` object | **resolved by design change** (D33): integrity is a fetch-time property |
| A2 | `alias` incompatible with `precompiled`; §10 wrong for `.mojoc` | **fixed**: the `.mojoc` file name is the import name, so an alias creates a second artifact; route in the key (D8/D37) |
| A3 | v0.1 covers three products (package manager, bundler, language abstraction) | **partly**: `run`/`install` are now ephemeral (§11a); `onefile` stays but is implemented last |
| A4 | `src/` clone race is a correctness bug, not performance | **moot** after D32 (project-local cache); P12 shrinks to stale-lock handling |
| W1 | Toolchain identity is the display name, not the build | **fixed**: fingerprint from `mojo --version` (D35) |
| W2 | `triple`/`flags` in the `.mojoc` key is over-engineering | **fixed**: removed (D24/D38); `.mojoc` is architecture-independent |
| W3 | `.mojoc` cache is wrong for hook packages | **fixed** as D40: pkgs with a `pre-build` hook are not artifact-cached |
| W4 | `src/` keyed by tag instead of commit | **fixed**: `<commit12>` only; tag lives in `meta.json` |
| W5 | Trust model mixes policy and consent | **open**: a deliberate v0.1 decision; `bojo.allow` is policy and is reviewed in the PR |
| W6 | D11 without MVS becomes a lockstep brake | **open**: R1 stays hard; two versions of one pkg are possible via `alias` on different routes |
| W7 | `precompiled = true` has no producer | **fixed**: field removed; bojo precompiles every pkg itself (D8) |
| W8 | `install` writing into a cache directory | **fixed**: `~/.bojo/bin`, outside the cache (D30) |
| W9 | Two discovery mechanisms (import list + scan) | **open**: `import` is primary, the scan is a documented fallback (D4) |
| W10 | `[repo] url` as identity breaks forks and mirrors | **open**: lock stores the URL; mirror rewriting is a later feature |
| N1 | The `Driver` abstraction is a fiction | **fixed**: internal Go struct, no public API (D34) |
| N2 | Four syntaxes for two concepts | **partly**: `[[deps-from]]` unified (D42); `deps`/`in-repo` remain |
| N3 | D1's Go rationale is overstated (`x/mod/modfile` parses `go.mod`, `lockedfile` is internal) | **fixed**: D1 reduced to what is verifiable; the resolver is hand-built |
| N4 | `-strip-file-prefix` is unverified | **rejected with evidence**: `mojo build --help-hidden` 1.2.0 lists it |
| N5 | Undocumented public surface (`BOJO_*`, `--strict-hooks`, ID canonicalisation, hook timeout, lock formatting) | **open** |
| N6 | `MODULAR_CACHE_DIR` per project creates N copies | **open**: accepted for isolation; revisit with P17 |
| N7 | `bojo verify` recomputes the resolver | **fixed**: verify checks self-consistency against a fresh checkout (D36) |

## Round 3 — changes caused by the project-local cache

| Finding | New assessment |
|---|---|
| B1 / A1 (hooks write into `src/`) | **resolved by semantics** (D33): the word "immutable" is gone from §8 |
| B21 / A4 (shared-cache races) | **moot** (D32); only the project lock remains |
| W4 / B13 (`src/` keyed by tag) | **fixed**: `<urlhash12>/<commit12>` |
| W1 (toolchain identity) | **fixed**: fingerprint (D35) |
| W2 (triple/flags in key) | **fixed**: removed (D24/D38) |
| B2 / A2 (alias × `.mojoc`) | **fixed**: second artifact per route (D8/D37) |
| W3 (hook output not in key) | **fixed**: D40 |
| B26 / B29 (`tree`, `cache verify`) | **fixed**: `git-tree:<OID>`, `bojo verify` against a fresh checkout (D36) |
| B20 / W8 (install target) | **fixed**: `~/.bojo/bin`, ephemeral build (D34a) |
| W5, W6, W9, W10, N5, N6 | **still open** |

## Known residual risks (v0.1)

1. No sandbox; approval is the only barrier (§12.3).
2. Hook chains hide inner execution (`build.rs`, CMake).
3. A committed allow line can pre-approve a dependency added in the same PR.
4. `PATH` can change between approval and execution (mitigated by D39).
5. Path deps are hashed, but a race inside the hash window remains.
