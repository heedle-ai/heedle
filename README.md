# heedle

A personal AI that runs on your own machine, and shows you what it will do before it does it.

You ask in plain English. Claude writes a small program: browse these sites, ask a question about what they say, fill in this form, run this skill. You see the program, and what each step may touch, before anything runs. It runs on [Temporal](https://temporal.io), every run is a commit in Git, and every browser runs in a throwaway [Podman](https://podman.io) container that can reach only the sites the program names.

- **Browsers side by side.** Several browsing steps run at once, and you watch each one live.
- **Forms stop before they send.** An act step fills a form, then waits: nothing is submitted until you press Send.
- **Your details stay yours.** Your name, email and address live in your keychain. Claude sees placeholders; heedle types the real values inside the browser.
- **Take the wheel.** When a site needs a sign-in or a code from your phone, Claude pauses, and you type in the same browser.
- **Replay.** A form that worked once replays without calling Claude, and still asks before it sends.
- **Costs and budgets.** Every Claude call is counted, and a schedule can have a monthly budget.

## Install

macOS on Apple Silicon:

```sh
brew install --cask heedle-ai/tap/heedle
heedle secret set ANTHROPIC_API_KEY     # your own Anthropic API key
heedle up                               # then open http://127.0.0.1:8090
```

On Linux (amd64, arm64), build it from source: see [Getting Started](https://github.com/heedle-ai/heedle/wiki/Getting-Started), which also covers what heedle needs (Git, Podman, the Temporal CLI). The [wiki](https://github.com/heedle-ai/heedle/wiki) explains how it all works.

## Status: early, and looking for testers

heedle is a work in progress, built by one person in evenings and weekends. It works end to end, and it has rough edges. Please try it and tell us what breaks.

### How much it has been tested

Honestly:

- **Automated tests, on every change.** Every repository's tests run in CI on Linux (amd64 and arm64) and macOS. They include about 700 invariants, each with a test that fails if the invariant breaks, property tests for the program algebra, and fuzzing of the program language.
- **End to end in CI.** heedle's CI starts a real Temporal server and runs heedle the way a fresh install would. It downloads the published images, fills in a course of eleven practice forms, replays one, hands a sign-in to a person, and runs two heedle homes at once. **These runs use a stand-in for Claude**, so they test heedle's plumbing, not Claude's judgement.
- **With real Claude:** one recorded session, against small local demo sites (the demo video). The browser agent has **not** been scored on the practice course with real Claude yet.
- **On a real Mac:** the author's own Apple Silicon Mac runs the brew install, `heedle up`, browsing real sites and running skills. Rough edges found there so far:
  - Podman's virtual machine stopped answering after the Mac slept. `heedle up` now detects this and says how to restart it.
  - Chromium inside Podman once failed with a network change.
  - A connector added from the MCP Registry turned out to need its own API key, which heedle didn't flag when it was added.
  - Clicking a button twice made a second skill version.
- **Not tested yet:**
  - a person installing on Linux (only CI has);
  - many real websites, with cookie walls and bot checks;
  - schedules running for weeks;
  - upgrading a home with months of history;
  - a security review by a person. The reviews so far were done by independent Claude sessions.

The [testing roadmap](https://github.com/heedle-ai/heedle/issues?q=is%3Aissue+label%3Atesting) lists what's next.

### How heedle is built

Most, if not all, of the code was written by Claude (Anthropic's model), working in Claude Code. The author designed every component and checked each design by hand. Every change goes through its repository's tests and CI, and significant changes get an independent review by a fresh Claude session before they're merged. Every mistake a review or a test finds becomes a written invariant with a test that guards it. Commits are authored by the author and co-authored by Claude.

## Help wanted

The most useful help right now, in order:

1. **Try it and report what breaks.** Install it, run something you'd really use it for, and [open an issue](https://github.com/heedle-ai/heedle/issues/new) with what you did and what happened. Reports from real websites and from Linux are especially welcome.
2. **Pick up a roadmap issue.** Every open roadmap item is an issue, labelled [`roadmap`](https://github.com/heedle-ai/heedle/issues?q=is%3Aissue+is%3Aopen+label%3Aroadmap). The ones marked [`good first issue`](https://github.com/heedle-ai/heedle/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22) are small and well described.
3. **Review the security boundaries.** The browser allowlist, the hold before a form sends, how your details stay out of Claude's view, and saved sign-ins. A careful human review is the gap we most want closed.

Each repository's `CLAUDE.md` has its rules, which apply to people too. Build on proven tools rather than rewriting them, keep the wiki current, and turn every mistake into a test.

## The repositories

heedle is one binary built from these, each its own repository:

| Repository | What it does |
|---|---|
| [weft](https://github.com/heedle-ai/weft) | The algebra programs are made of |
| [draft](https://github.com/heedle-ai/draft) | The program language |
| [shuttle](https://github.com/heedle-ai/shuttle) | Runs programs on Temporal |
| [bobbin](https://github.com/heedle-ai/bobbin) | Containers, through Podman |
| [warp](https://github.com/heedle-ai/warp) | The Git store for runs |
| [reed](https://github.com/heedle-ai/reed) | A fresh browser for each browsing step |
| [pirn](https://github.com/heedle-ai/pirn) | Claude: questions, agents, the browser agent |
| [treadle](https://github.com/heedle-ai/treadle) | Skills |
| [loom](https://github.com/heedle-ai/loom) | Runs it all together; the command line |
| [drawdown](https://github.com/heedle-ai/drawdown) | The web UI |
| heedle | This one: `heedle up`, `heedle down`, and the release |

MIT licence.
