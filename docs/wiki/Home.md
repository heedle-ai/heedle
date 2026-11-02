# heedle

heedle-ai in one binary. `heedle up` starts everything on your machine: Temporal, a worker and the UI. Every loom command (`run`, `compile`, `worker`, `write`, `writes`, `skills`, `branch`, `merge`, `beliefs`) is a heedle command too.

- [Getting Started](Getting-Started): build heedle, run `heedle up`, run a program.
- [How It Works](How-It-Works): what `heedle up` starts, where it keeps things, and how heedle is built and released.
- [Invariants](Invariants): what is guaranteed, and the test or CI step that keeps it so.

heedle adds no behaviour of its own beyond `up`. It is [loom](https://github.com/heedle-ai/loom/wiki)'s command-line tool and [drawdown](https://github.com/heedle-ai/drawdown/wiki)'s UI in one program.
