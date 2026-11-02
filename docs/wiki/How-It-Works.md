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

`.goreleaser.yaml` builds heedle for Linux (amd64, arm64) and macOS on Apple Silicon. It writes a Homebrew cask for `heedle-ai/homebrew-tap` that depends on `git`, `podman` and `temporal`. CI checks the configuration and builds a snapshot on every push (`ci:release`). Nothing is uploaded yet.

**Planned:** publishing releases and the tap, once bobbin, reed, pirn and treadle are public. A published binary contains their code.
