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
//	                               secret, schedule, schedules, costs
//	heedle version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
  heedle containers [-home DIR] [-stop WORKFLOW [-run RUN] | -clean]
                       heedle's containers, their homes and runs; stop a
                       run, or remove this home's containers of runs that
                       have ended
  heedle images [-home DIR] [-prune]
                       heedle's images; remove the ones this release
                       doesn't pin and no container uses
  heedle ui -store DIR [FLAGS]
                       the UI and a worker alone, against a Temporal you run
  heedle ask [-then run|schedule] "WHAT YOU WANT DONE"
                       heedle proposes how, says what it needs, and runs or
                       schedules it if you say so
  heedle COMMAND ...   a loom command: run, compile, worker, write, ask,
                       writes, forms, skills, branch, merge, beliefs,
                       secret, memory, schedule, schedules, costs (heedle
                       COMMAND -h for its flags)
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
		// gave its worker, and every running home's (roadmap 9.2.1).
		home, rest, err := homeArg(args[1:])
		if err != nil {
			return err
		}
		return cli.Run(ctx, append(append([]string{"images"}, keepArgs(ctx, system{}.withDefaults(out), home)...), rest...), out)
	case "containers":
		// loom's containers, for this home: its store, so -clean removes
		// only this home's containers and the listing marks them, and its
		// Temporal (roadmap 9.2.1).
		home, rest, err := homeArg(args[1:])
		if err != nil {
			return err
		}
		return cli.Run(ctx, append(append([]string{"containers"}, homeArgs(home, rest)...), rest...), out)
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

// homeArg takes heedle images' and containers' -home (the home heedle up
// was started with; the default if none) out of its arguments, leaving
// loom's (HEEDLE-507), as an absolute path.
func homeArg(args []string) (string, []string, error) {
	home, rest := defaultHome(), []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		name, value, inline := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if name != "home" || !strings.HasPrefix(args[i], "-") {
			rest = append(rest, args[i])
			continue
		}
		if !inline {
			if i+1 >= len(args) {
				return "", nil, errors.New("-home needs a directory")
			}
			i++
			value = args[i]
		}
		home = value
	}
	return absHome(home), rest, nil
}

// keepArgs are -keep flags for the images heedle images never prunes:
// this release's pins, the images the running heedle up (home's up.json)
// gave its worker, and those of every other home whose heedle up runs
// now, its release's pins included (roadmap 9.2.1): so a prune for one
// home never removes an image another running home pinned.
func keepArgs(ctx context.Context, sys system, home string) []string {
	refs := pinned.refs()
	for _, h := range append([]string{home}, otherHomes(ctx, sys, home)...) {
		if st, err := readUpState(h); err == nil && st != nil {
			refs = append(refs, st.Pins...)
			refs = append(refs, st.Keep...)
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range refs {
		if r != "" && !seen[r] {
			seen[r] = true
			out = append(out, "-keep", r)
		}
	}
	return out
}

// homeArgs are the flags heedle containers gives loom's for home, unless
// given: its store (-store), and the Temporal its running heedle up
// started (-address, from up.json).
func homeArgs(home string, rest []string) []string {
	var out []string
	if !hasFlag(rest, "store") {
		out = append(out, "-store", filepath.Join(home, "store.git"))
	}
	if st, err := readUpState(home); err == nil && st != nil && st.TemporalPort > 0 && !hasFlag(rest, "address") {
		out = append(out, "-address", net.JoinHostPort("127.0.0.1", strconv.Itoa(st.TemporalPort)))
	}
	return out
}
