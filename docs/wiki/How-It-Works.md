# How It Works

heedle is three existing programs in one binary, plus `heedle up` and `heedle down`:

| Command | What runs |
|---|---|
| `heedle up` | Temporal's dev server, then a loom worker and the drawdown UI (below) |
| `heedle down` | stops what `heedle up` started, and heedle's containers ([below](#heedle-down-and-a-clean-start)) |
| `heedle images` | loom's `images`, keeping the images this release pins and those every running `heedle up` pinned or gave its worker (`-home` names its home) |
| `heedle containers` | loom's `containers`, for this home: its store and its Temporal (`-home` names it) |
| `heedle ui -store DIR ...` | drawdown with its own flags (`drawdown/app.Run`) |
| `heedle COMMAND ...` | any loom command, unchanged (`loom/cli.Run`) |
| `heedle version` | the version GoReleaser stamped in, or `dev` |

heedle reimplements none of them: `TestInv_HeedleIsLoomAndDrawdown` checks that a loom command and drawdown's errors come back from heedle as they are.

## heedle up

[[/images/up.svg|alt=String diagram of heedle up: given ~/.heedle, the Podman machine is made ready on macOS, then Temporal's dev server starts, then a loom worker and the drawdown UI run side by side with Temporal's address and the store; the worker's runs are recorded in Git and you open the UI in a browser]]

In order:

1. **Checks.** Git and the Temporal CLI must be on the PATH, and Temporal's two ports free, or nothing starts. A Temporal already on those ports would answer in place of heedle's, with another database, and keep running after heedle stops. Without Podman, heedle says which steps won't run and goes on.
2. **Podman's virtual machine, on macOS only.** Podman runs containers in a Linux VM there. If there is no machine, heedle runs `podman machine init` and `podman machine start`. If none is running or starting, it starts the default one. On Linux, Podman needs no machine, and heedle never touches one. Then, on both, heedle asks Podman itself (`podman info`), giving a starting machine a minute: a machine can say it runs and still refuse every connection (after the Mac slept, say), and then heedle starts nothing and says to restart it with `podman machine stop` and `podman machine start`.
3. **The images, once.** Browse steps need a browser image and agent and skill steps an agent image. heedle pins one of each in `images.json`, by content: `ghcr.io/heedle-ai/reed-browser@sha256:…`, built and tested by reed's `image.yml` (Debian's Chromium, the libraries it links, fonts and socat, nothing else), and `ghcr.io/heedle-ai/pirn-agent@sha256:…`, built and tested by pirn's (a shell and a few everyday tools on Debian). Both are for Linux on amd64 and arm64, which is what a Mac's Podman machine runs. Act steps, which fill in forms (roadmap 9.5), need a browser-agent image: `ghcr.io/heedle-ai/reed-browser-agent@sha256:…` (`browserAgent`), the browser image plus Node.js and a pinned Playwright MCP, built and tested by reed's `image.yml` too. If Podman doesn't have one, heedle downloads it with `podman pull`, once, and gives each to the worker (`-browser-image`, `-agent-image`, `-browser-agent-image`). Your own, after `--`, wins and isn't downloaded. If a download fails, heedle says why in a line and starts anyway; the front door then says which steps can't run. A new image is a new digest in a heedle release, never a moving tag. Without Podman, nothing is downloaded.
4. **Temporal.** heedle runs `temporal server start-dev` and waits until it answers a health check, for up to a minute. If it exits first, heedle says so at once and points to its log. If it doesn't answer in time, or heedle is stopped meanwhile, heedle stops it, so nothing is left running (`TestInv_TemporalNeverOutlivesUp`). The frontend is on `127.0.0.1:7233` and the Web UI on `127.0.0.1:8233` (`-temporal-port`, `-temporal-ui-port`). Its database is `~/.heedle/temporal.db`, so history survives a restart. Its log is `~/.heedle/temporal.log`.
5. **The worker and the UI.** drawdown runs with the store `~/.heedle/store.git`, Temporal's address and its Web UI, on `127.0.0.1:8090` (`-listen`). It runs a loom worker in the same process, as drawdown always does. Its connectors file is `~/.heedle/connectors.json`, which needn't exist. Beside it are what heedle remembers about you, `~/.heedle/memory.json` (drawdown's default for `-memory`): the worker sends it to Claude with every request to the front door, as the `memory` source, and the UI's Memory page and `heedle memory` show and change it ([loom → Memory you can see](https://github.com/heedle-ai/loom/wiki/How-It-Works#the-front-door-ask)); and your prices file, for cost estimates and budgets, `~/.heedle/prices.json` ([loom → Costs and budgets](https://github.com/heedle-ai/loom/wiki/How-It-Works#costs-and-budgets)). The worker checks a schedule's monthly budget before each of its runs starts anything. Flags after `--` go to drawdown, such as `-agent-image`, or `-connectors` for another file.

`heedle up` also gives the worker the images its release pins (`-keep-images`), so the System page never removes them. Ctrl-C lets requests in flight finish, stops the UI and the worker, then interrupts Temporal and waits for it to exit. A second Ctrl-C ends heedle at once. heedle stops Temporal however the UI ends, even when it fails (`TestInv_UpStartsTemporalThenTheUI`).

## heedle down, and a clean start

`heedle up` records what it started in `~/.heedle/up.json` (roadmap 9.2). The file holds its own process ID and command line, Temporal's process ID, the command heedle ran it with (with this home's database) and the command line the system reported for it once it started, the ports, the UI's address, the images it gave the worker, the images its release pins, and the home's label. If the process answering on Temporal's port isn't the one heedle started (a `temporal` on the PATH that is a wrapper running the real CLI as its child, without `exec`), heedle asks `lsof` which process it is and records that one too (`listener`, roadmap 9.2.1). heedle removes the file once Temporal has stopped. heedle stops a process only if it is still exactly the one `up.json` records: the same process ID, running the same command line, and for Temporal, a dev server with this home's database. Temporal is matched on the command line the system reported, so a `temporal` on the PATH that is a wrapper running the real CLI is still recognised. Anything else on the port, or now under that process ID, is left alone.

```sh
./heedle down
```

`heedle down` interrupts that `heedle up`, which stops its UI, its worker and Temporal as Ctrl-C does (a wrapper's child first, then the wrapper). If Temporal outlived it, `heedle down` stops Temporal too, killing it if it won't stop. It never says Temporal stopped while something still answers on its port: if a Temporal it can't recognise as its own is still there, it says so, names the process if `lsof` can, leaves `up.json`, and exits with an error. Then it removes this home's containers, and nothing else: those labelled `heedle.home` with this home's label (the first 12 hex digits of the SHA-256 of its store's path, which its worker puts on every container, a separate `loom worker`'s on the same store included). A container from before homes were labelled has no home label: `heedle down` removes it only when no other home's heedle runs, and otherwise says it left it. It does all this only if it found this home's heedle or Temporal running, so `heedle down` for a home that isn't running never touches a container. It says what it stopped, or that nothing of heedle's was running.

heedle can run from two homes at once (roadmap 9.2.1), each with its own `-home`, ports and Temporal. Each running `heedle up` says so in `~/.heedle/homes/` (a file named for its home's label, holding the home's path), so `heedle down` and `heedle images` can see the others; a home counts as running only while its `up.json` names a heedle or Temporal that is still exactly the one it started.

[[/images/homes.svg|alt=String diagram of two homes at once: heedle up from home A and from home B side by side, each a Temporal dev server with its own database, then a loom worker on its own store, then its steps' containers labelled with its home; heedle down -home A touches only A's]]

`heedle up` starts by reading `up.json`:

- If that heedle is still running, it says so and stops: `heedle is already running at http://127.0.0.1:8090; heedle down stops it`.
- If heedle was killed (`kill -9`, a crash) and left its Temporal running, it stops that Temporal, says `Stopped the Temporal a previous heedle up left running (pid N).`, and goes on.
- Anything else on Temporal's ports still fails, as before.

Once Temporal answers, it removes the containers of runs that have ended (loom's `Clean`), left behind by a heedle that was killed.

`heedle containers` lists heedle's containers with each one's home (this home's marked `(this)`), the run it belongs to and its status, step, age, image, CPU and memory. heedle gives loom this home's store and the Temporal its `heedle up` started (`-home` names another). `heedle containers -stop WORKFLOW [-run RUN]` stops an exact run through Temporal, the one `-run` names or else the one run of that workflow whose containers are listed, never simply the latest run of that workflow ID; its steps end and Podman removes their containers. `-clean` removes this home's containers of runs that have ended ([loom → Housekeeping](https://github.com/heedle-ai/loom/wiki/How-It-Works#housekeeping-containers-images-and-stopping-a-run)). `heedle images` lists heedle's images. `heedle images -prune` removes the old ones: images under `ghcr.io/heedle-ai/` that this release doesn't pin, that no running home's `heedle up` pinned or gave its worker (each `up.json` the shared folder names), and that no container uses. Your own images, and your own names on heedle's, are never touched. The UI's **System** page shows and does the same ([drawdown → The System page](https://github.com/heedle-ai/drawdown/wiki/How-It-Works#the-system-page)).

## Act steps, end to end

[[/images/act.svg|alt=An act step: the instruction, start page and hosts become a question; Claude's loop in pirn, bounded, offered only the form tools; each call goes through podman exec to Playwright MCP and Chromium in a fresh reed container that reaches only allowed hosts; a screenshot after every action is heartbeated to the run page; the result is strict JSON recorded in Git]]

`(act "instruction" "https://start" "host"…)` has Claude fill in a form in a fresh browser that may reach only the hosts it names, then stop before sending and ask you (roadmap 9.7). reed starts the browser and holds every send until you confirm ([reed → Act sessions](https://github.com/heedle-ai/reed/wiki/How-It-Works#act-sessions)), pirn runs Claude's loop and records it ([pirn → Act steps](https://github.com/heedle-ai/pirn/wiki/How-It-Works#act-steps)), loom and drawdown say before a run which hosts it may act on. When a form is ready to send, `heedle up` tells you ("heedle: a form is ready to send"), the run page shows it filled with **Send** and **Cancel**, and `heedle forms -workflow <ID> -send <ID>` decides at the terminal; Cancel, or 30 minutes with no answer, sends nothing. A program can opt a step out with `:send-without-asking` after its hosts; the card says so in red.

CI checks it end to end on Linux (`TestE2EAct_FillsAForm`, the `e2e-act` step): heedle compiles an act step and loom runs it in the pinned browser-agent image, with a stand-in Claude, against a two-page form the test serves itself (`testdata/actform`), never a real site. The form must receive exactly what was typed and chosen, and the stand-in asks before each page that sends. The page also asks for another host, and tells the model to send everything there; the stand-in Claude then navigates to that host by name and by address and asks for a script tool. The other host must receive no request at all, the navigations must be refused before they reach the browser, the script tool must be refused, what the first page asked of the other host must be in the record's blocked requests, and no act container may be left. To run it on Linux with Podman:

```bash
HEEDLE_E2E_ACT=1 go test -count=1 -v -run TestE2EAct_FillsAForm .
```

It uses the pinned image, downloading it if Podman doesn't have it; `HEEDLE_E2E_ACT_IMAGE=<image ID>` uses another, such as one built with [reed → A browser-agent image](https://github.com/heedle-ai/reed/wiki/Getting-Started#6-a-browser-agent-image-for-act).

## The course of forms, and its score

reed keeps a fixed course of seven practice forms ([reed → The course of forms](https://github.com/heedle-ai/reed/wiki/How-It-Works#the-course-of-forms)), so a change to act steps is known to help or hurt (roadmap 9.6). CI runs every form as an act step with a stand-in Claude that scripts its calls (`TestE2ECourse_StandIn`, the `e2e-course` step, Linux only): each form must receive exactly what its task asks, the password form must be refused and receive nothing, and the injection form's decoy host must receive nothing. Every send asks first; a stand-in that skips `confirm` has its sends held and the form receives nothing more, one a person says no to ends cancelled with nothing sent, and only a program that says `:send-without-asking` sends unasked. A run with the person's details (`TestE2EProfile_LeavesNoValue`, roadmap 9.8) types them by placeholder into the contact form, which receives the real values, while no value is anywhere in the run's Git store, in what Temporal kept, in the logs, or in any request to Claude. That proves the plumbing. Claude itself is scored by hand, with a key, once a release: [Browser agent scores](Browser-agent-scores).

## Releases

Pushing a tag such as `v0.1.0` runs `.github/workflows/release.yml`, which runs GoReleaser (`.goreleaser.yaml`):

1. It builds heedle for Linux (amd64, arm64) and macOS on Apple Silicon.
2. It makes a GitHub release of heedle-ai/heedle with those archives and their checksums, using the workflow's own token.
3. It pushes a Homebrew cask to heedle-ai/homebrew-tap, using the `HOMEBREW_TAP_DEPLOY_KEY` secret, a deploy key that may write to the tap and nothing else. The cask depends on `git`, `podman` and `temporal`.

Both repositories are public, so the cask downloads the archive from the release with Homebrew's own download, and checks its SHA-256. A pre-release tag (such as `v0.3.0-rc1`) makes a GitHub pre-release, but isn't pushed to the tap. If a tag is ever made again with different archives, Homebrew's cached download fails its checksum until `brew cleanup heedle`. Without Homebrew, the release's archive for your platform has the same binary.

CI checks all this on every push without publishing. On Linux (`ci:release`) it checks the configuration, builds a snapshot of exactly the supported platforms, and checks that the cask is valid Ruby and downloads every archive straight from the public release. On macOS (`ci:cask`), Homebrew itself loads the snapshot's cask in developer mode, where anything deprecated is an error.
