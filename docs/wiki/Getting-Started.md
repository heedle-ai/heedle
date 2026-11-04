# Getting Started

heedle runs on Linux (amd64, arm64) and on macOS on Apple Silicon.

## What you need

- Go 1.25 or later, to build it.
- Git 2.42 or later.
- [Podman](https://podman.io), for `container`, `agent` and `browse` steps. Programs without those steps run without it. On macOS, `heedle up` creates and starts Podman's virtual machine for you. On first run, on macOS and Linux, it downloads the ready-made browser and agent images it gives its worker (see [How It Works](How-It-Works#heedle-up)).
- The [Temporal CLI](https://docs.temporal.io/cli), on the PATH: `brew install temporal` on macOS, or `curl -sSf https://temporal.download/cli.sh | sh` on Linux.
- For `ask` and `agent` steps, an Anthropic API key: stored once with `heedle secret set ANTHROPIC_API_KEY`, or in `ANTHROPIC_API_KEY` where heedle runs. On Linux the keychain needs `secret-tool` (`libsecret-tools` on Debian and Ubuntu) and a running secret service such as GNOME Keyring.

## Build it

heedle builds against its sibling repositories, so clone them side by side. All of them are public, under the MIT licence.

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

```sh
brew install --cask heedle-ai/tap/heedle
heedle version
```

Homebrew finds the tap (heedle-ai/homebrew-tap) by its name. This also installs Git, Podman and the Temporal CLI; `brew upgrade --cask heedle` upgrades it.

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

`ask` and `agent` steps use `ANTHROPIC_API_KEY` from the environment `heedle up` runs in, or else from your keychain. Store it, or a connector's key, once:

```sh
./heedle secret set ANTHROPIC_API_KEY
```

It asks for the value without showing it, and keeps it in the macOS Keychain or the Linux secret service under the service `heedle`. Or paste it on the UI's **connectors** page, which keeps it in the same keychain. Workers read keys for every step, so a key stored while heedle runs is used from the next step, with no restart. `heedle up` keeps the worker's connectors file at `~/.heedle/connectors.json` (until it exists, there are no connectors), and the worker reads it before every step too. The connectors page adds servers to it from the MCP Registry, signing you in where a server offers it (open heedle at `http://127.0.0.1:8090` for that), with their tokens going to your keychain and refreshed for you. Connectors that name a secret (`"tokenEnv": "MAPS_KEY"`) find it there from their next step, with no restart. Other programs running as you can read keychain items too; what the keychain keeps keys out of is records, logs and the agents' containers. `./heedle secret delete MAPS_KEY` removes one. `heedle up` sets `-store`, `-address`, `-temporal-ui` and `-listen` itself, and refuses them after `--`.

## Ask heedle

The home page opens with **What should heedle do?** Say what you want in plain words:

> Plan three days in Lisbon from 12 December. I like food, tiles and viewpoints.

heedle's Claude answers with what it would do: a summary, the program it would run (drawn as a graph), and what that needs, each ready or not, with **Connect** beside anything missing. It uses the skills you've approved and the commands heedle has; if nothing fits, it says which skill would, and **Teach heedle this skill** has Claude write it for your review. Ask again to change it ("make it four days", "every Monday at 8"); a request that says when gets a button that schedules it. Nothing runs until you press **Run it now** or the schedule button. Asking needs the Anthropic key, like `ask` steps.

From the terminal, with `heedle up` running:

```sh
./heedle ask "Every weekday at 8, check whether shipment 4471 will be late"
./heedle ask -then run "Plan three days in Lisbon from 12 December. I like food and tiles."
```

It prints the proposal and a line for each thing it needs, ready or missing with how to set it up. `-then run` runs it and `-then schedule` schedules it as proposed, each only when everything it needs is ready.

## Notifications

While `heedle up` runs, it tells you with your system's own notifications when a scheduled run ends (and, if it failed, that its schedule is paused until you look) and when a write waits for your approval, each once, with the address of the run. On macOS it uses `osascript`; on Linux `notify-send` (Debian and Ubuntu: `sudo apt install libnotify-bin`). A run you start yourself doesn't notify. When many things happen at once, it shows five and one more saying how many others there are. If it can't reach Temporal it says so once in its output. `heedle up -notify=false` turns them off.

## Run programs from the terminal

With `heedle up` running, every loom command works against it:

```sh
./heedle compile '(pipe (echo "a") echo)'       # prints the plan; runs nothing
./heedle run '(pipe (par (echo "a") (echo "b")) echo)'
./heedle beliefs -store ~/.heedle/store.git
```

`./heedle COMMAND -h` lists a command's flags; [loom → Getting Started](https://github.com/heedle-ai/loom/wiki/Getting-Started) explains them.

## Plan a trip

The trip planner is a skill and two connectors, with no code: treadle's [`trip-planner`](https://github.com/heedle-ai/treadle/tree/main/skills/trip-planner). It researches a destination with a search connector and builds a day-by-day itinerary with a maps connector: places, their opening hours that day, and travel times between them.

1. **Connect search and maps.** On the **connectors** page, find a web search server and a maps server in the MCP Registry, and add each, signing in or pasting a key. Name them `search` and `maps`: the skill asks for those names.
2. **Add the skill.** On the **skills** page, import `https://github.com/heedle-ai/treadle/tree/main/skills/trip-planner`, or upload its folder. It asks for connectors, so it waits for your review: the review shows `search maps` in red. Approve it.
3. **Ask for a trip** on the home page ("Plan three days in Lisbon from 12 December. I like food, tiles and viewpoints."), and heedle proposes the trip planner with those arguments, or **run it** yourself:

```lisp
(trip-planner "Lisbon" "2026-12-12" "2026-12-16" "food, tiles, viewpoints")
```

The run's output is the itinerary: a section for each day, its stops with times and addresses, notes on closures and holidays, and the pages it used. It only looks things up: it books, buys and saves nothing. To make sure of that whatever Claude does, list your maps connector's saving tools under `writes`, with `approve`, in `~/.heedle/connectors.json`; then any call to one waits for you. **Schedule…** it weekly to see what's on.

## Run a program on a schedule

Write a program on the home page, then **Schedule…** beside **Run**: give it a name, and run it every hour, day or week, or at cron times in your time zone. The **schedules** page runs one now, pauses, resumes or deletes it. From the terminal:

```sh
./heedle schedule -name weekday-digest -cron "0 9 * * 1-5" -tz Europe/Lisbon '(pipe (echo "weekly digest") echo)'
./heedle schedules
```

Temporal keeps the schedules, so they run while `heedle up` runs, and a run missed by under 10 minutes (your Mac asleep) still starts when it's back. "Every day" runs at the time of day you made it; use cron times for a set hour. A run that fails pauses its schedule, and the schedules page says why.

## Uninstall

heedle keeps its keys in your keychain, every one under the service `heedle`, and everything else in `~/.heedle`. Delete the keys first, while heedle is still installed, then heedle and its home:

```sh
./heedle secret delete --all
brew uninstall --cask heedle
rm -rf ~/.heedle
```

`secret delete --all` asks before it deletes at a terminal, and doesn't from a script. It removes only items filed under `heedle`: in the macOS Keychain, one at a time, counting them; in the Linux secret service, with `secret-tool clear service heedle`, then checks none is left (a locked keyring keeps them, and it says so). With heedle already gone, the same can be done by hand: on macOS, `while security delete-generic-password -s heedle >/dev/null 2>&1; do :; done`; on Linux, `secret-tool clear service heedle`.
