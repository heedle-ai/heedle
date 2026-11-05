// Command heedle is heedle-ai in one binary: the loom command-line tool,
// the drawdown UI and a loom worker, and heedle up, which starts Temporal,
// a worker and the UI together.
//
//	heedle up                      start everything; open http://127.0.0.1:8090
//	heedle down                    stop everything heedle up started
//	heedle ui -store DIR ...       the UI (and a worker) alone: drawdown's flags
//	heedle ask "REQUEST"           ask for something; heedle proposes how
//	heedle run PROGRAM ...         any loom command: run, compile, worker, write,
//	                               ask, writes, skills, branch, merge, beliefs,
//	                               secret, schedule, schedules
//	heedle version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/heedle-ai/drawdown/app"
	"github.com/heedle-ai/loom/cli"
)

// version is set at build time (GoReleaser's ldflags); "dev" otherwise.
var version = "dev"

const usage = `usage:
  heedle up [-home DIR] [-listen ADDR] [-temporal-port N] [-temporal-ui-port N] [-- UI FLAGS]
                       start Temporal, a worker and the UI (open the URL it prints)
  heedle down [-home DIR]
                       stop what heedle up started, and heedle's containers
  heedle containers [-stop WORKFLOW | -clean]
                       heedle's containers and their runs; stop a run, or
                       remove the containers of runs that have ended
  heedle images [-prune]
                       heedle's images; remove the ones this release
                       doesn't pin and no container uses
  heedle ui -store DIR [FLAGS]
                       the UI and a worker alone, against a Temporal you run
  heedle ask [-then run|schedule] "WHAT YOU WANT DONE"
                       heedle proposes how, says what it needs, and runs or
                       schedules it if you say so
  heedle COMMAND ...   a loom command: run, compile, worker, write, ask,
                       writes, skills, branch, merge, beliefs, secret,
                       schedule, schedules (heedle COMMAND -h for its flags)
  heedle version`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// A second Ctrl-C while shutting down ends the process at once.
	go func() { <-ctx.Done(); stop() }()
	err := run(ctx, os.Args[1:], os.Stdout)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "heedle:", err)
		os.Exit(1)
	}
}

// run dispatches one heedle command. Everything that isn't heedle's own
// (up, ui, version, help) is loom's, unchanged.
func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing command\n" + usage)
	}
	switch args[0] {
	case "up":
		return up(ctx, args[1:], out, system{})
	case "down":
		return down(ctx, args[1:], out, system{})
	case "images":
		// loom's images, keeping this release's pins and what heedle up
		// gave its worker.
		return cli.Run(ctx, append(append([]string{"images"}, keepArgs(defaultHome())...), args[1:]...), out)
	case "ui":
		return app.Run(ctx, args[1:], out)
	case "version", "-version", "--version":
		fmt.Fprintln(out, "heedle", version)
		return nil
	case "help", "-h", "-help", "--help":
		fmt.Fprintln(out, usage)
		return nil
	}
	return cli.Run(ctx, args, out)
}

// keepArgs are -keep flags for the images heedle images never prunes:
// this release's pins, and the images the running heedle up (home's
// up.json) gave its worker.
func keepArgs(home string) []string {
	refs := pinned.refs()
	if st, err := readUpState(home); err == nil && st != nil {
		refs = append(refs, st.Keep...)
	}
	var out []string
	for _, r := range refs {
		out = append(out, "-keep", r)
	}
	return out
}
