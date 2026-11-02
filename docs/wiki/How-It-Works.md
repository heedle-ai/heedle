# How It Works

heedle is three existing programs in one binary, plus `heedle up`:

| Command | What runs |
|---|---|
| `heedle up` | Temporal's dev server, then a loom worker and the drawdown UI (below) |
| `heedle ui -store DIR ...` | drawdown with its own flags (`drawdown/app.Run`) |
| `heedle COMMAND ...` | any loom command, unchanged (`loom/cli.Run`) |
| `heedle version` | the version GoReleaser stamped in, or `dev` |

heedle reimplements none of them: `TestInv_HeedleIsLoomAndDrawdown` checks that a loom command and drawdown's errors come back from heedle as they are.

## heedle up

[[/images/up.svg|alt=String diagram of heedle up: given ~/.heedle, the Podman machine is made ready on macOS, then Temporal's dev server starts, then a loom worker and the drawdown UI run side by side with Temporal's address and the store; the worker's runs are recorded in Git and you open the UI in a browser]]

In order:

1. **Checks.** Git and the Temporal CLI must be on the PATH, and Temporal's two ports free, or nothing starts. A Temporal already on those ports would answer in place of heedle's, with another database, and keep running after heedle stops. Without Podman, heedle says which steps won't run and goes on.
2. **Podman's virtual machine, on macOS only.** Podman runs containers in a Linux VM there. If there is no machine, heedle runs `podman machine init` and `podman machine start`. If none is running or starting, it starts the default one. On Linux, Podman needs no machine, and heedle never touches one.
3. **Temporal.** heedle runs `temporal server start-dev` and waits until it answers a health check, for up to a minute. If it exits first, heedle says so at once and points to its log. If it doesn't answer in time, or heedle is stopped meanwhile, heedle stops it, so nothing is left running (`TestInv_TemporalNeverOutlivesUp`). The frontend is on `127.0.0.1:7233` and the Web UI on `127.0.0.1:8233` (`-temporal-port`, `-temporal-ui-port`). Its database is `~/.heedle/temporal.db`, so history survives a restart. Its log is `~/.heedle/temporal.log`.
4. **The worker and the UI.** drawdown runs with the store `~/.heedle/store.git`, Temporal's address and its Web UI, on `127.0.0.1:8090` (`-listen`). It runs a loom worker in the same process, as drawdown always does. Flags after `--` go to drawdown, such as `-agent-image` and `-connectors`.

Ctrl-C lets requests in flight finish, stops the UI and the worker, then interrupts Temporal and waits for it to exit. A second Ctrl-C ends heedle at once. heedle stops Temporal however the UI ends, even when it fails (`TestInv_UpStartsTemporalThenTheUI`).

## Releases

Pushing a tag such as `v0.1.0` runs `.github/workflows/release.yml`, which runs GoReleaser (`.goreleaser.yaml`):

1. It builds heedle for Linux (amd64, arm64) and macOS on Apple Silicon.
2. It makes a GitHub release of heedle-ai/heedle with those archives and their checksums, using the workflow's own token.
3. It pushes a Homebrew cask to heedle-ai/homebrew-tap, using the `HOMEBREW_TAP_DEPLOY_KEY` secret, a deploy key that may write to the tap and nothing else. The cask depends on `git`, `podman` and `temporal`.

Both repositories are private, and a release contains bobbin's, reed's, pirn's and treadle's code, so only people with access can install it. A private release's download URL answers 404 without a token, so the cask downloads with `HeedlePrivateReleaseDownloadStrategy`, defined in the cask itself. When it downloads, it takes the token from `HOMEBREW_GITHUB_API_TOKEN`, or else from `gh auth token`. It passes the token to curl in a file only you can read, never in curl's arguments. It looks up the archive's asset ID with GitHub's REST API and downloads the asset through that API. GitHub redirects to a signed URL on another host, and curl doesn't send the token there. Homebrew still checks the archive's SHA-256. A pre-release tag (such as `v0.2.0-rc1`) makes a GitHub pre-release, but isn't pushed to the tap. If a tag is ever made again with different archives, Homebrew's cached download fails its checksum until `brew cleanup heedle`. It builds on Homebrew's `CurlDownloadStrategy`, which isn't a public API, so a Homebrew update may need it adjusted. Without Homebrew, `gh release download --repo heedle-ai/heedle --pattern '*darwin_arm64.tar.gz'` gets the same archive.

CI checks all this on every push without publishing. On Linux (`ci:release`) it checks the configuration, builds a snapshot of exactly the supported platforms, and checks that the cask is valid Ruby and downloads every archive through that strategy. On macOS (`ci:cask`), Homebrew itself loads the snapshot's cask in developer mode, where anything deprecated is an error.
