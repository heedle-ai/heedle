package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/heedle-ai/bobbin"
	"github.com/heedle-ai/loom"
)

// Housekeeping (roadmap 9.2): heedle up records what it started in
// up.json, in its home; heedle down stops it; and the next heedle up
// cleans up what a heedle that was killed left behind. heedle stops a
// process only if it is still exactly the one up.json records: the same
// process ID running the same command line, and for Temporal, this home's
// database. Anything else, on the port or under the same process ID, is
// left alone.

// upFile is the record of a running heedle up, in its home.
const upFile = "up.json"

// upState is what heedle up started.
type upState struct {
	PID  int      `json:"pid"`  // heedle up's own process
	Args []string `json:"args"` // its command line
	Home string   `json:"home"`
	// Temporal is the dev server heedle up started: its process ID, the
	// command heedle ran (the CLI's path, then its arguments), and the
	// command line the system then reported for it, which differs when
	// the CLI on the PATH is a wrapper that runs the real one (HEEDLE-509).
	Temporal struct {
		PID     int      `json:"pid"`
		Args    []string `json:"args"`
		Cmdline string   `json:"cmdline,omitempty"`
	} `json:"temporal"`
	TemporalPort   int       `json:"temporalPort"`
	TemporalUIPort int       `json:"temporalUIPort"`
	UI             string    `json:"ui"`   // the UI's address, http://…
	Keep           []string  `json:"keep"` // the images the worker was given
	Started        time.Time `json:"started"`
}

func writeUpState(home string, st upState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(home, upFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(home, upFile))
}

// readUpState reads home's up.json; nil if there is none.
func readUpState(home string) (*upState, error) {
	b, err := os.ReadFile(filepath.Join(home, upFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st upState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(home, upFile), err)
	}
	return &st, nil
}

// readProcArgs is a process's command line, its arguments joined by
// spaces, as the system reports it: /proc on Linux, ps on macOS.
func readProcArgs(ctx context.Context, sys system, pid int) (string, error) {
	if sys.goos == "linux" {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return "", err
		}
		return strings.Join(strings.Split(strings.TrimRight(string(b), "\x00"), "\x00"), " "), nil
	}
	b, err := sys.output(ctx, "ps", "-ww", "-o", "args=", "-p", strconv.Itoa(pid))
	return strings.TrimSpace(string(b)), err
}

// isProcess reports whether pid is alive and still runs exactly line, its
// command line as the system reports it: so a process ID reused by
// something else is never taken for heedle's.
func isProcess(ctx context.Context, sys system, pid int, line string) bool {
	if pid <= 0 || line == "" || sys.signal(pid, syscall.Signal(0)) != nil {
		return false
	}
	got, err := sys.procArgs(ctx, pid)
	return err == nil && got == line
}

// temporalLine is st's Temporal's command line as the system reported it
// once started; for an up.json written before that was recorded, the
// command heedle ran.
func (st *upState) temporalLine() string {
	if st.Temporal.Cmdline != "" {
		return st.Temporal.Cmdline
	}
	return strings.Join(st.Temporal.Args, " ")
}

// ourTemporal reports whether st's Temporal is still running, and is
// exactly the dev server heedle up started with home's database: heedle
// ran it so, and the process still says so.
func ourTemporal(ctx context.Context, sys system, st *upState, home string) bool {
	db := filepath.Join(home, "temporal.db")
	i := slices.Index(st.Temporal.Args, "--db-filename")
	if i < 0 || i+1 >= len(st.Temporal.Args) || st.Temporal.Args[i+1] != db || !slices.Contains(st.Temporal.Args, "start-dev") {
		return false
	}
	line := st.temporalLine()
	if !strings.Contains(line, " start-dev ") || !strings.Contains(line, " --db-filename "+db) {
		return false
	}
	return isProcess(ctx, sys, st.Temporal.PID, line)
}

// stopProcess interrupts pid, which must still run line, and waits for it
// to go: then, if kill, kills it. It reports whether it's gone.
func stopProcess(ctx context.Context, sys system, pid int, line string, wait time.Duration, kill bool) bool {
	gone := func() bool {
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			if !isProcess(ctx, sys, pid, line) {
				return true
			}
			time.Sleep(100 * time.Millisecond)
		}
		return !isProcess(ctx, sys, pid, line)
	}
	_ = sys.signal(pid, os.Interrupt)
	if gone() {
		return true
	}
	// heedle ends at once on a second interrupt; Temporal is killed.
	sig := os.Interrupt
	if kill {
		sig = os.Kill
	}
	if !isProcess(ctx, sys, pid, line) {
		return true
	}
	_ = sys.signal(pid, sig)
	return gone()
}

// cleanStart is heedle up's first step: if home's heedle is running, it
// says so (heedle down stops it); if only the Temporal a killed heedle
// left is, it stops it. Anything else on Temporal's ports still fails, as
// before.
func cleanStart(ctx context.Context, home string, sys system, out io.Writer) error {
	st, err := readUpState(home)
	if err != nil || st == nil {
		return err
	}
	if isProcess(ctx, sys, st.PID, strings.Join(st.Args, " ")) {
		return fmt.Errorf("heedle is already running at %s; heedle down stops it", st.UI)
	}
	if ourTemporal(ctx, sys, st, home) {
		if !stopProcess(ctx, sys, st.Temporal.PID, st.temporalLine(), 15*time.Second, true) {
			return fmt.Errorf("the Temporal a previous heedle up left running (pid %d) didn't stop", st.Temporal.PID)
		}
		fmt.Fprintf(out, "Stopped the Temporal a previous heedle up left running (pid %d).\n", st.Temporal.PID)
	}
	return os.Remove(filepath.Join(home, upFile))
}

// sweep removes heedle's containers whose run has ended, now Temporal is
// up (loom.Clean): left by a heedle that was killed, say.
func sweep(ctx context.Context, hostPort string, out io.Writer) {
	c, err := client.Dial(client.Options{HostPort: hostPort, Logger: quiet{}})
	if err != nil {
		return
	}
	defer c.Close()
	gone, err := loom.Clean(ctx, c, bobbin.Runner{})
	if err != nil {
		fmt.Fprintf(out, "Couldn't look for containers left by runs that ended: %v\n", err)
		return
	}
	if len(gone) > 0 {
		fmt.Fprintf(out, "Removed %d container%s left by runs that had ended.\n", len(gone), plural(len(gone)))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// down stops what heedle up started from home: heedle up itself (which
// stops its worker, UI and Temporal), Temporal if it outlived it, then
// heedle's labelled containers, but only if it found this home's heedle
// or Temporal running: so heedle down for a home that isn't running never
// removes the containers of a heedle running from another (HEEDLE-508).
// It says what it stopped.
func down(ctx context.Context, args []string, out io.Writer, sys system) error {
	sys = sys.withDefaults(out)
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	fs.SetOutput(out)
	home := fs.String("home", defaultHome(), "the home heedle up was started with")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	var did []string
	st, err := readUpState(*home)
	if err != nil {
		return err
	}
	if st != nil {
		if line := strings.Join(st.Args, " "); isProcess(ctx, sys, st.PID, line) {
			if !stopProcess(ctx, sys, st.PID, line, time.Minute, false) {
				return fmt.Errorf("heedle (pid %d) didn't stop", st.PID)
			}
			did = append(did, fmt.Sprintf("heedle (pid %d, %s) and its worker", st.PID, st.UI))
		}
		if ourTemporal(ctx, sys, st, *home) {
			if !stopProcess(ctx, sys, st.Temporal.PID, st.temporalLine(), 15*time.Second, true) {
				return fmt.Errorf("Temporal (pid %d) didn't stop", st.Temporal.PID)
			}
			did = append(did, fmt.Sprintf("Temporal (pid %d)", st.Temporal.PID))
		} else if len(did) > 0 {
			did[len(did)-1] += ", with its Temporal"
		}
		if err := os.Remove(filepath.Join(*home, upFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if len(did) == 0 {
		fmt.Fprintln(out, "Nothing of heedle's was running.")
		return nil
	}
	if podman, err := sys.lookPath("podman"); err == nil {
		gone, err := bobbin.Runner{Podman: podman}.Remove(ctx, func(bobbin.Container) bool { return true })
		if err != nil {
			return fmt.Errorf("removing heedle's containers: %w", err)
		}
		if len(gone) > 0 {
			did = append(did, fmt.Sprintf("%d of heedle's containers", len(gone)))
		}
	}
	fmt.Fprintf(out, "Stopped %s.\n", strings.Join(did, "; "))
	return nil
}
