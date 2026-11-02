# Getting Started

heedle runs on Linux (amd64, arm64) and on macOS on Apple Silicon.

## What you need

- Go 1.24 or later, to build it.
- Git 2.42 or later.
- [Podman](https://podman.io), for `container`, `agent` and `browse` steps. Programs without those steps run without it. On macOS, `heedle up` creates and starts Podman's virtual machine for you.
- The [Temporal CLI](https://docs.temporal.io/cli), on the PATH: `brew install temporal` on macOS, or `curl -sSf https://temporal.download/cli.sh | sh` on Linux.
- For `ask` and `agent` steps, an Anthropic API key in `ANTHROPIC_API_KEY`, where heedle runs.

## Build it

heedle builds against its sibling repositories, so clone them side by side. bobbin, reed, pirn and treadle are private for now, so you need access to them.

```sh
mkdir heedle-ai && cd heedle-ai
for r in weft draft shuttle warp bobbin reed pirn treadle loom drawdown heedle; do
  git clone https://github.com/heedle-ai/$r.git
done
cd heedle
go build -o heedle .
./heedle version
```

### Or install it with Homebrew (macOS on Apple Silicon)

heedle's releases and its tap are private, so Homebrew downloads with your GitHub token. You need access to heedle-ai/heedle and heedle-ai/homebrew-tap.

```sh
export HOMEBREW_GITHUB_API_TOKEN=$(gh auth token)
brew tap heedle-ai/tap git@github.com:heedle-ai/homebrew-tap.git
brew install --cask heedle-ai/tap/heedle
heedle version
```

This also installs Git, Podman and the Temporal CLI. Keep `HOMEBREW_GITHUB_API_TOKEN` set when you upgrade (`brew upgrade --cask heedle`).

## Start everything

```sh
./heedle up
```

It prints where everything is: Temporal's address and Web UI, Temporal's log, and the UI's address. If another Temporal is already running on its ports (say, a `temporal server start-dev` from loom's Getting Started), it refuses: stop that one, or give `-temporal-port` and `-temporal-ui-port`. Open `http://127.0.0.1:8090` and run a program from **New run**, for example:

```
(pipe (par (echo "a") (echo "b")) echo)
```

Press Ctrl-C to stop the UI, the worker and Temporal. Runs, skills and Temporal's history are kept in `~/.heedle`; give `-home DIR` to keep them elsewhere.

To give the UI drawdown's own flags, such as `-agent-image` or `-connectors`, put them after `--`. For example, to record runs on another history:

```sh
./heedle up -- -ref refs/loom/research
```

`ask` and `agent` steps use `ANTHROPIC_API_KEY` from the environment `heedle up` runs in. `heedle up` sets `-store`, `-address`, `-temporal-ui` and `-listen` itself, and refuses them after `--`.

## Run programs from the terminal

With `heedle up` running, every loom command works against it:

```sh
./heedle compile '(pipe (echo "a") echo)'       # prints the plan; runs nothing
./heedle run '(pipe (par (echo "a") (echo "b")) echo)'
./heedle beliefs -store ~/.heedle/store.git
```

`./heedle COMMAND -h` lists a command's flags; [loom → Getting Started](https://github.com/heedle-ai/loom/wiki/Getting-Started) explains them.
