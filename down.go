package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
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
		// Listener is the process that answers on Temporal's port when it
		// isn't the one heedle started: a temporal on the PATH that is a
		// wrapper which runs the real one as its child, without exec
		// (roadmap 9.2.1). Stopping the wrapper would leave it running.
		Listener *process `json:"listener,omitempty"`
	} `json:"temporal"`
	TemporalPort   int       `json:"temporalPort"`
	TemporalUIPort int       `json:"temporalUIPort"`
	UI             string    `json:"ui"`   // the UI's address, http://…
	Keep           []string  `json:"keep"` // the images the worker was given
	Started        time.Time `json:"started"`
	// HomeLabel is the home's label on every container its worker starts
	// (bobbin.HomeOf its store; roadmap 9.2.1), and Pins the images this
	// heedle release pins, so another home's heedle images -prune keeps
	// them too.
	HomeLabel string   `json:"homeLabel,omitempty"`
	Pins      []string `json:"pins,omitempty"`
}

// process is a process heedle may stop: its ID and its command line as
// the system reported it.
type process struct {
	PID     int    `json:"pid"`
	Cmdline string `json:"cmdline"`
}

// homeLabel is the label of home's containers: the one its store has
// (bobbin.HomeOf), never taken on trust from up.json (review 55).
func (st *upState) homeLabel(home string) string {
	return bobbin.HomeOf(filepath.Join(home, "store.git"))
}

// readHomeState is home's up.json, if it is this home's: one copied from
// another home (with the home folder, while it ran) names that home's
// processes, so it is never acted on here (review 55).
func readHomeState(home string) (*upState, error) {
	st, err := readUpState(home)
	if err != nil || st == nil {
		return st, err
	}
	if st.Home != "" && absHome(st.Home) != home {
		return nil, fmt.Errorf("%s is %s's, not this home's: it was copied here, so heedle leaves it alone (remove it if that heedle isn't running)", filepath.Join(home, upFile), st.Home)
	}
	return st, nil
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
	if !devServerOf(line, db) {
		return false
	}
	return isProcess(ctx, sys, st.Temporal.PID, line)
}

// ourListener reports whether st records a listener (a wrapper's child)
// that still runs exactly its recorded command line, a dev server with
// home's database (roadmap 9.2.1).
func ourListener(ctx context.Context, sys system, st *upState, home string) bool {
	l := st.Temporal.Listener
	if l == nil || l.PID == st.Temporal.PID {
		return false
	}
	if !devServerOf(l.Cmdline, filepath.Join(home, "temporal.db")) {
		return false
	}
	return isProcess(ctx, sys, l.PID, l.Cmdline)
}

// devServerOf reports whether line, a command line as the system reports
// it, is a dev server (start-dev) with exactly db as its database: whole
// arguments, so .../temporal.db-x is another database (review 55).
func devServerOf(line, db string) bool {
	line += " "
	return strings.Contains(line, " start-dev ") && strings.Contains(line, " --db-filename "+db+" ")
}

// errStillAnswers says something answers on Temporal's port that heedle
// can't tell is the Temporal it started.
func errStillAnswers(ctx context.Context, sys system, port int) error {
	who := ""
	if pid, err := sys.listener(ctx, port); err == nil {
		who = fmt.Sprintf(" (pid %d)", pid)
	}
	return fmt.Errorf("something still answers on Temporal's port %d%s, which heedle couldn't tell was the Temporal it started, so it's still running: stop it yourself", port, who)
}

// portAnswers reports whether st's Temporal port answers.
func (st *upState) portAnswers() bool {
	return st.TemporalPort > 0 && answers(net.JoinHostPort("127.0.0.1", strconv.Itoa(st.TemporalPort)))
}

// listening is the process that listens on 127.0.0.1:port, as lsof
// reports it (on Linux and macOS alike); 0 with an error when it can't
// say.
func listening(ctx context.Context, sys system, port int) (int, error) {
	lsof, err := sys.lookPath("lsof")
	if err != nil {
		return 0, errors.New("lsof isn't on the PATH")
	}
	b, err := sys.output(ctx, lsof, "-nP", "-a", "-iTCP@127.0.0.1:"+strconv.Itoa(port), "-sTCP:LISTEN", "-t")
	if err != nil && len(b) == 0 {
		return 0, fmt.Errorf("lsof found no process listening on port %d", port)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("lsof said %q", first)
	}
	return pid, nil
}

// answers reports whether something accepts connections on addr.
func answers(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
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
	st, err := readHomeState(home)
	if err != nil || st == nil {
		return err
	}
	if isProcess(ctx, sys, st.PID, strings.Join(st.Args, " ")) {
		return fmt.Errorf("heedle is already running at %s; heedle down stops it", st.UI)
	}
	// A wrapper's child outlives the wrapper (roadmap 9.2.1); it goes
	// first, and the wrapper ends as it does.
	if ourListener(ctx, sys, st, home) {
		l := st.Temporal.Listener
		if !stopProcess(ctx, sys, l.PID, l.Cmdline, 15*time.Second, true) {
			return fmt.Errorf("the Temporal a previous heedle up left running (pid %d) didn't stop", l.PID)
		}
		fmt.Fprintf(out, "Stopped the Temporal a previous heedle up left running (pid %d).\n", l.PID)
	}
	if ourTemporal(ctx, sys, st, home) {
		if !stopProcess(ctx, sys, st.Temporal.PID, st.temporalLine(), 15*time.Second, true) {
			return fmt.Errorf("the Temporal a previous heedle up left running (pid %d) didn't stop", st.Temporal.PID)
		}
		fmt.Fprintf(out, "Stopped the Temporal a previous heedle up left running (pid %d).\n", st.Temporal.PID)
	}
	// up.json goes only once nothing answers on its Temporal's port: one
	// heedle couldn't recognise is said, and up.json kept (review 55).
	if st.portAnswers() {
		return errStillAnswers(ctx, sys, st.TemporalPort)
	}
	return os.Remove(filepath.Join(home, upFile))
}

// knownOld are heedle's containers without a home label (from before
// roadmap 9.2.1) whose run the Temporal at hostPort knows: this home's.
// Asked of Temporal before heedle stops it; none if it can't say.
func knownOld(ctx context.Context, hostPort, podman string) map[string]bool {
	known := map[string]bool{}
	c, err := client.Dial(client.Options{HostPort: hostPort, Logger: quiet{}, ConnectionOptions: client.ConnectionOptions{GetSystemInfoTimeout: 5 * time.Second}})
	if err != nil {
		return known
	}
	defer c.Close()
	cs, err := loom.Containers(ctx, c, bobbin.Runner{Podman: podman})
	if err != nil {
		return known
	}
	for _, ct := range cs {
		if ct.Labels.Home == "" && ct.Status != "" && ct.Status != "unknown" {
			known[ct.ID] = true
		}
	}
	return known
}

// sweep removes this home's containers whose run has ended, now Temporal
// is up (loom.Clean): left by a heedle that was killed, say. Another
// home's are never touched (roadmap 9.2.1).
func sweep(ctx context.Context, hostPort, home string, out io.Writer) {
	c, err := client.Dial(client.Options{HostPort: hostPort, Logger: quiet{}})
	if err != nil {
		return
	}
	defer c.Close()
	gone, err := loom.Clean(ctx, c, bobbin.Runner{}, home)
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
// stops its worker, UI and Temporal), Temporal if it outlived it (the
// process heedle started, and the one answering on its port if a wrapper
// started that one), then this home's containers, but only if it found
// this home's heedle or Temporal running: so heedle down for a home that
// isn't running never removes a container (HEEDLE-508). Another home's
// containers are never removed, and one from before homes were labelled
// only when no other home's heedle runs (roadmap 9.2.1). It says what it
// stopped, and never that Temporal stopped while something still answers
// on its port.
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
	*home = absHome(*home)
	var did []string
	var still error
	st, err := readHomeState(*home)
	if err != nil {
		return err
	}
	label := st.homeLabel(*home)
	// A container from before homes were labelled may be any home's,
	// even one whose heedle is older than homes and says nothing: it goes
	// only if this home's Temporal knows its run, asked before it stops
	// (review 55).
	podman, perr := sys.lookPath("podman")
	known := map[string]bool{}
	if st != nil && perr == nil && st.portAnswers() {
		known = knownOld(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(st.TemporalPort)), podman)
	}
	if st != nil {
		if line := strings.Join(st.Args, " "); isProcess(ctx, sys, st.PID, line) {
			if !stopProcess(ctx, sys, st.PID, line, time.Minute, false) {
				return fmt.Errorf("heedle (pid %d) didn't stop", st.PID)
			}
			did = append(did, fmt.Sprintf("heedle (pid %d, %s) and its worker", st.PID, st.UI))
		}
		temporal, wrapper := false, -1
		// The Temporal a wrapper started as its child outlives the
		// wrapper (roadmap 9.2.1). It goes first: the wrapper then ends
		// as its child does.
		if ourListener(ctx, sys, st, *home) {
			l := st.Temporal.Listener
			if !stopProcess(ctx, sys, l.PID, l.Cmdline, 15*time.Second, true) {
				return fmt.Errorf("Temporal (pid %d) didn't stop", l.PID)
			}
			did, temporal = append(did, fmt.Sprintf("Temporal (pid %d, started by its wrapper)", l.PID)), true
		}
		if ourTemporal(ctx, sys, st, *home) {
			if !stopProcess(ctx, sys, st.Temporal.PID, st.temporalLine(), 15*time.Second, true) {
				return fmt.Errorf("Temporal (pid %d) didn't stop", st.Temporal.PID)
			}
			what := "Temporal"
			if st.Temporal.Listener != nil {
				what = "Temporal's wrapper"
			}
			did, temporal, wrapper = append(did, fmt.Sprintf("%s (pid %d)", what, st.Temporal.PID)), true, len(did)
		}
		if !temporal && len(did) > 0 {
			did[len(did)-1] += ", with its Temporal"
		}
		// Never say Temporal stopped while something still answers on its
		// port, whether or not heedle found anything of its own running:
		// up.json stays, and the next heedle down says so again (roadmap
		// 9.2.1, review 55).
		if st.portAnswers() {
			still = errStillAnswers(ctx, sys, st.TemporalPort)
			if len(did) > 0 && !temporal {
				did[len(did)-1] = strings.TrimSuffix(did[len(did)-1], ", with its Temporal")
			} else if wrapper >= 0 {
				did[wrapper] = fmt.Sprintf("the process heedle started Temporal with (pid %d)", st.Temporal.PID)
			}
		} else if err := os.Remove(filepath.Join(*home, upFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if len(did) == 0 {
		if still != nil {
			return still
		}
		fmt.Fprintln(out, "Nothing of heedle's was running.")
		return nil
	}
	if perr == nil {
		var left int
		gone, err := bobbin.Runner{Podman: podman}.Remove(ctx, func(c bobbin.Container) bool {
			if c.Labels.Home == "" && !known[c.ID] {
				left++
			}
			return c.Labels.Home == label || c.Labels.Home == "" && known[c.ID]
		})
		if err != nil {
			return fmt.Errorf("removing heedle's containers: %w", err)
		}
		if len(gone) > 0 {
			did = append(did, fmt.Sprintf("%d of this home's containers", len(gone)))
		}
		if left > 0 {
			defer fmt.Fprintf(out, "Left %d container%s from before heedle labelled homes whose run this home's Temporal doesn't know: they may be another home's (heedle containers lists them).\n", left, plural(left))
		}
	}
	fmt.Fprintf(out, "Stopped %s.\n", strings.Join(did, "; "))
	return still
}
