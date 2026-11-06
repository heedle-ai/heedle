# heedle

heedle-ai in one binary. `heedle up` starts everything on your machine: Temporal, a worker and the UI. Every loom command (`run`, `compile`, `worker`, `write`, `writes`, `skills`, `branch`, `merge`, `beliefs`) is a heedle command too.

- [Getting Started](Getting-Started): build heedle, run `heedle up`, run a program.
- [How It Works](How-It-Works): what `heedle up` starts, where it keeps things, and how heedle is built and released.
- [Invariants](Invariants): what is guaranteed, and the test or CI step that keeps it so.

heedle adds no behaviour of its own beyond `up` and `down`. It is [loom](https://github.com/heedle-ai/loom/wiki)'s command-line tool and [drawdown](https://github.com/heedle-ai/drawdown/wiki)'s UI in one program.

## Status

heedle is early: it works end to end, with rough edges, and it is looking for testers. The [README](https://github.com/heedle-ai/heedle#status-early-and-looking-for-testers) says honestly how much it has been tested (end to end in CI with a stand-in for Claude; with real Claude in one recorded session; on the author's Mac), how it is built (most of the code by Claude, every design checked by hand), and where help is wanted. Open work is in the [roadmap issues](https://github.com/heedle-ai/heedle/issues?q=is%3Aissue+is%3Aopen+label%3Aroadmap).
