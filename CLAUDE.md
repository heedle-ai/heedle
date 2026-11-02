# heedle

heedle-ai in one binary: loom's command-line tool, drawdown's UI and a
worker, and `heedle up`, which starts Temporal, a worker and the UI. Part
of heedle-ai:
weft (algebra) → draft (language) → shuttle (execution) → bobbin (runtime) →
warp (object store) → drawdown (UI); reed gives browsing
agents a browser; pirn gives them Claude; loom runs them together.

## The first rule: reuse, never rewrite

Build on battle-tested tools (Git, Temporal, the Go standard library and
other long-proven software) through their public interfaces. Add on top of
them; never fork, patch, mutate or reimplement them. Prefer the real tool to
a reimplementation of it (for example the `git` CLI over a Go port of Git).
If a task seems to need a rewrite, stop and ask the owner first, explaining
why the existing tool can't do it.

## Rules for every change

1. **Keep the wiki current.** Any change that affects how a developer
   installs, builds, tests, uses or understands this repo updates
   `docs/wiki/` in the same commit or PR. That folder is the source of truth;
   `.github/workflows/wiki.yml` mirrors it to
   https://github.com/heedle-ai/heedle/wiki on every push to `main`. Never edit
   the wiki on GitHub directly: the next sync overwrites it. Code, README and
   wiki must never disagree.
2. **Wiki quality bar.** Every command is copy-pasteable and has been run.
   Every code snippet compiles and is checked by a test; prefer quoting a
   Go `Example` test with an `// Output:` comment. State required versions.
   No placeholders, TODOs or "coming soon" on published pages. Mark design
   that isn't built yet as **Planned**, plainly.
3. **Platforms: Linux (amd64, arm64) and macOS on Apple Silicon only.** Don't
   write Windows instructions, scripts, CI jobs or path handling.
4. **Bare minimum.** Add only what the task needs. No speculative packages,
   dependencies or abstractions.
5. **Tests pass before commit:** `go vet ./...` and `go test -race ./...`.
6. **Codify every mistake.** A mistake isn't fixed until it has a row in
   `docs/wiki/Invariants.md` (ID, invariant, guard, where it came from) and
   a guard that fails on the old code: a test, a fuzz seed, or a CI step
   (`ci:NAME`). `bash .github/invariants.sh` checks the page against the
   code in both directions and runs in CI. Name guard tests `TestLaw_*` or
   `TestInv_*`; every `Fuzz*` test is a guard. Keep inputs that ever broke a
   fuzz test in `testdata/fuzz/`.
7. **Show, don't tell.** Explain structure with string diagrams: SVGs in
   `docs/wiki/images/` that work in light and dark mode, using `;` for
   "then" and `⊗` for "side by side".
8. **Get an independent review before significant pushes.** A fresh
   reviewer that hasn't seen the work tries to break it; every finding
   becomes an invariant row.

## Repo-specific

- **heedle adds nothing but `up`.** Every other command is `loom/cli.Run`
  or `drawdown/app.Run`, unchanged; change behaviour there, not here
  (`TestInv_HeedleIsLoomAndDrawdown`).
- **`heedle up` uses the real tools:** the `temporal` CLI's dev server
  (run directly, so heedle can see it exit and stop it), and `podman
  machine` on macOS. It never reimplements or patches them.
- **Releases are GoReleaser's** (`.goreleaser.yaml`). Nothing is published
  until bobbin, reed, pirn and treadle are public; CI's `release` step
  only checks and builds a snapshot.
- **Run every CI step before pushing,** not only the Go tests:
  `bash .github/invariants.sh`, and `goreleaser check` when the release
  configuration changes. The `e2e` step needs a real Temporal server, so
  it runs in CI.
- `go.mod` replaces the heedle-ai modules with `../` checkouts until they are
  published; clone all repos as siblings.
