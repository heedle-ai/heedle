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
3. **The images, once.** Browse steps need a browser image and agent and skill steps an agent image. heedle pins one of each in `images.json`, by content: `ghcr.io/heedle-ai/reed-browser@sha256:…`, built and tested by reed's `image.yml` (Debian's Chromium, the libraries it links, fonts and socat, nothing else), and `ghcr.io/heedle-ai/pirn-agent@sha256:…`, built and tested by pirn's (a shell and a few everyday tools on Debian). Both are for Linux on amd64 and arm64, which is what a Mac's Podman machine runs. If Podman doesn't have one, heedle downloads it with `podman pull`, once, and gives both to the worker (`-browser-image`, `-agent-image`). Your own, after `--`, wins and isn't downloaded. If a download fails, heedle says why in a line and starts anyway; the front door then says which steps can't run. A new image is a new digest in a heedle release, never a moving tag. Without Podman, nothing is downloaded.
4. **Temporal.** heedle runs `temporal server start-dev` and waits until it answers a health check, for up to a minute. If it exits first, heedle says so at once and points to its log. If it doesn't answer in time, or heedle is stopped meanwhile, heedle stops it, so nothing is left running (`TestInv_TemporalNeverOutlivesUp`). The frontend is on `127.0.0.1:7233` and the Web UI on `127.0.0.1:8233` (`-temporal-port`, `-temporal-ui-port`). Its database is `~/.heedle/temporal.db`, so history survives a restart. Its log is `~/.heedle/temporal.log`.
5. **The worker and the UI.** drawdown runs with the store `~/.heedle/store.git`, Temporal's address and its Web UI, on `127.0.0.1:8090` (`-listen`). It runs a loom worker in the same process, as drawdown always does. Its connectors file is `~/.heedle/connectors.json`, which needn't exist. Flags after `--` go to drawdown, such as `-agent-image`, or `-connectors` for another file.

Ctrl-C lets requests in flight finish, stops the UI and the worker, then interrupts Temporal and waits for it to exit. A second Ctrl-C ends heedle at once. heedle stops Temporal however the UI ends, even when it fails (`TestInv_UpStartsTemporalThenTheUI`).

## Releases

Pushing a tag such as `v0.1.0` runs `.github/workflows/release.yml`, which runs GoReleaser (`.goreleaser.yaml`):

1. It builds heedle for Linux (amd64, arm64) and macOS on Apple Silicon.
2. It makes a GitHub release of heedle-ai/heedle with those archives and their checksums, using the workflow's own token.
3. It pushes a Homebrew cask to heedle-ai/homebrew-tap, using the `HOMEBREW_TAP_DEPLOY_KEY` secret, a deploy key that may write to the tap and nothing else. The cask depends on `git`, `podman` and `temporal`.

Both repositories are public, so the cask downloads the archive from the release with Homebrew's own download, and checks its SHA-256. A pre-release tag (such as `v0.3.0-rc1`) makes a GitHub pre-release, but isn't pushed to the tap. If a tag is ever made again with different archives, Homebrew's cached download fails its checksum until `brew cleanup heedle`. Without Homebrew, the release's archive for your platform has the same binary.

CI checks all this on every push without publishing. On Linux (`ci:release`) it checks the configuration, builds a snapshot of exactly the supported platforms, and checks that the cask is valid Ruby and downloads every archive straight from the public release. On macOS (`ci:cask`), Homebrew itself loads the snapshot's cask in developer mode, where anything deprecated is an error.
