# Code review — implementation round 1

Second review pass, this time against the Go implementation (commit `b7c4cd2`).
Nine blockers were raised. Disposition below; anything still open is listed at the
end and is a known gap, not an oversight.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| C1 | blocker | Approved hooks were never executed — the loop only printed them | **fixed**: `runHooks` runs `pre-build` before `mojo build` and `post-build` after, foreign hooks in the package dir, own hooks in the project root |
| C2 | blocker | Cross-repo cycles/diamonds re-enqueued dependencies forever | **fixed**: a repository's own `deps-from` is expanded once per `(repo, commit)` |
| C3 | blocker | `--locked` never compared the lock; exit 5 unreachable | **fixed**: `compareLock` compares name/commit sets; verified end to end (exit 5) |
| C4 | blocker | Version bump always failed with "tree mismatch" | **fixed**: `lock.TreesByCommit()` keys expected trees by `<repo>@<commit>` |
| C5 | blocker | `verify` reused the working tree, so it verified nothing | **fixed**: `Store.VerifyFresh` clones into a temp dir and compares there (D36) |
| C6 | blocker | Stale allow lines were both honoured and reported as ignored | **fixed**: `Store.Filter(locked)` before the gate; the stale note lists only filtered-out entries |
| C7 | blocker | `run`/`install` could never pass a hook gate | **fixed**: the approval store and `pending.json` now belong to the invoking project, not the temp dir |
| C8 | blocker | The project's own `[hooks]` were parsed but never run or shown | **fixed**: `runHooks` runs them in the project root and prints them |
| C9 | blocker | `bojo update` panicked on a corrupt lock | **fixed**: the error is handled and returns exit 1 |
| C10 | blocker | Route names could escape the module root (`..`, absolute) | **fixed**: `validateRoute` rejects absolute paths, `..`, empty segments, `\` and `:` |
| W1 | warning | No project lock; fixed `.new`/`.old` raced | **fixed**: `build.Lock` (PID file, stale detection) around steps 4–10; unique `MkdirTemp` for the module tree |
| W2 | warning | Module-root swap not atomic | **accepted**: the window is now covered by the project lock. `renameat2`/`renamex_np` is a POST-V0.1 option |
| W3 | warning | `output = "onefile"` was silently ignored | **fixed**: returns exit 1 with a clear message until the stub exists |
| W4 | warning | `install --force` was printed but not parsed | **fixed** |
| W5 | warning | `run` collapsed the program's exit code into 3 | **fixed**: the program's own exit code is propagated |
| W6 | warning | Network errors mapped to exit 1 | **fixed**: `cache.FetchError` for every `ls-remote`/fetch call |
| W7 | warning | Duplicate package names across repos were silently merged | **fixed**: same repo+commit+dir is a no-op, anything else is an error (R6) |
| W8 | warning | `lock.mojo` was written empty | **fixed**: `update` writes the toolchain fingerprint (D35) |
| W9 | note | `install` copy was not atomic | **fixed**: temp name + rename |

## Still open (deliberate, tracked)

| # | Finding | Why it is open |
|---|---|---|
| O1 | Build step 10 was incomplete: no library closure, no `$ORIGIN` RPATH, no `codesign` | **done (D52)**: closure is copied to `out/lib`, relative RPATH is passed at link time; verified by hiding the toolchain libraries and running the binary |
| O2 | No `.mojoc` precompile, no `obj/` cache, no `<deps12>` | Needs the same toolchain. The spec keeps the layout; the code does not implement it yet. |
| O3 | Route conflicts against project-local top-level directories are not checked (R1, second half) | Cheap to add, not yet done. |
| O4 | Pre-release ordering is lexicographic (`beta.10 < beta.2`) | No pre-release tags exist yet; fix before the first `-rc`. |
| O5 | Commit-prefix resolution only sees ref tips and does not peel tag objects | Needs a full fetch; acceptable while tags are the primary path. |
| O6 | No `meta.json` in the cache, so `bojo cache list` shows bare hashes | Cosmetic, but §8 promises it. |
| O7 | `[repo]` in a non-root package file and `[repo.mojo] min/max` are not enforced | Needs a toolchain comparison helper. |
| O8 | `bojo get` appends TOML textually instead of parse→mutate→encode | Loses no comments this way; a package name containing `.` would produce invalid TOML. |
| O9 | Bare tags (without `pkg/`) are skipped silently instead of with a warning | §6 asks for one line. |
| O10 | Module symlinks are relative now, but `run`/`install` verify the tree only at resolution time | §11a wording needs to match. |
