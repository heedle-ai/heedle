# Getting Started

heedle runs on Linux (amd64, arm64) and on macOS on Apple Silicon.

## What you need

- Go 1.24 or later, to build it.
- Git 2.42 or later.
- [Podman](https://podman.io), for `container`, `agent` and `browse` steps. Programs without those steps run without it. On macOS, `heedle up` creates and starts Podman's virtual machine for you.
- The [Temporal CLI](https://docs.temporal.io/cli) (`brew install temporal` on macOS). Without it on the PATH, `heedle up` uses the copy Temporal's Go SDK downloads.
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

**Planned:** `brew install heedle-ai/tap/heedle`, once those repositories are public. The release configuration is in `.goreleaser.yaml`; see [How It Works → Releases](How-It-Works#releases).

## Start everything

```sh
./heedle up
```

It prints where everything is: Temporal's address and Web UI, Temporal's log, and the UI's address. Open `http://127.0.0.1:8090` and run a program from **New run**, for example:

```
(pipe (par (echo "a") (echo "b")) echo)
```

Press Ctrl-C to stop the UI, the worker and Temporal. Runs, skills and Temporal's history are kept in `~/.heedle`; give `-home DIR` to keep them elsewhere.

To give the UI drawdown's own flags, put them after `--`:

```sh
ANTHROPIC_API_KEY=sk-ant-... ./heedle up -- -agent-image IMAGE-ID -connectors connectors.json
```

## Run programs from the terminal

With `heedle up` running, every loom command works against it:

```sh
./heedle compile '(pipe (echo "a") echo)'       # prints the plan; runs nothing
./heedle run '(pipe (par (echo "a") (echo "b")) echo)'
./heedle beliefs -store ~/.heedle/store.git
```

`./heedle COMMAND -h` lists a command's flags; [loom → Getting Started](https://github.com/heedle-ai/loom/wiki/Getting-Started) explains them.
