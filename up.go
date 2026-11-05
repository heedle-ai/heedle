package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"

	"github.com/heedle-ai/bobbin"
	"github.com/heedle-ai/drawdown/app"
	"github.com/heedle-ai/loom"
)

// system is what heedle up uses from the machine: programs on the PATH,
// Temporal's dev server and the UI. Its zero value is the real machine;
// tests replace parts.
type system struct {
	goos     string
	lookPath func(string) (string, error)
	// output runs a program and returns its standard output.
	output func(ctx context.Context, name string, args ...string) ([]byte, error)
	// combined runs a program and returns its standard output and error
	// together, so heedle can say why it failed in its own words.
	combined func(ctx context.Context, name string, args ...string) ([]byte, error)
	// temporal starts Temporal's dev server (path, with args), writing its
	// output to log, waits until its frontend at hostPort answers, and
	// returns a function that stops it. If it can't, nothing is left
	// running.
	temporal func(ctx context.Context, path string, args []string, log io.Writer, hostPort string) (stop func() error, pid int, err error)
	// serve serves the UI and a worker until ctx ends (drawdown's app.Run).
	serve func(ctx context.Context, args []string, out io.Writer) error
	// notify shows one of the operating system's notifications.
	notify func(ctx context.Context, title, body string) error
	// notifyEvery is how often heedle looks for something to tell.
	notifyEvery time.Duration
	// signal sends a process a signal; signal 0 says whether it's alive.
	signal func(pid int, sig os.Signal) error
	// procArgs is a process's command line, its arguments joined by
	// spaces.
	procArgs func(ctx context.Context, pid int) (string, error)
	// podmanRetry is how long heedle up waits between asking Podman
	// whether it answers.
	podmanRetry time.Duration
	// listener is the process listening on 127.0.0.1:port (lsof).
	listener func(ctx context.Context, port int) (int, error)
}

func (s system) withDefaults(out io.Writer) system {
	if s.goos == "" {
		s.goos = runtime.GOOS
	}
	if s.podmanRetry == 0 {
		s.podmanRetry = 2 * time.Second
	}
	if s.lookPath == nil {
		s.lookPath = exec.LookPath
	}
	if s.output == nil {
		s.output = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stderr = out
			return cmd.Output()
		}
	}
	if s.combined == nil {
		s.combined = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	if s.temporal == nil {
		s.temporal = startTemporal
	}
	if s.serve == nil {
		s.serve = app.Run
	}
	if s.notify == nil {
		s.notify = func(ctx context.Context, title, body string) error {
			name, args, err := notifyCommand(s.goos, s.lookPath, title, body)
			if err != nil {
				return err
			}
			_, err = s.output(ctx, name, args...)
			return err
		}
	}
	if s.notifyEvery == 0 {
		s.notifyEvery = 15 * time.Second
	}
	if s.signal == nil {
		s.signal = func(pid int, sig os.Signal) error {
			p, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			return p.Signal(sig)
		}
	}
	if s.procArgs == nil {
		s.procArgs = func(ctx context.Context, pid int) (string, error) { return readProcArgs(ctx, s, pid) }
	}
	if s.listener == nil {
		s.listener = func(ctx context.Context, port int) (int, error) { return listening(ctx, s, port) }
	}
	return s
}

// notifyCommand is the command that shows a notification: osascript on
// macOS, notify-send on Linux. The title and body are its arguments,
// never part of a script, so what they say can't change what runs.
func notifyCommand(goos string, lookPath func(string) (string, error), title, body string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "osascript", []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "--", title, body}, nil
	case "linux":
		if _, err := lookPath("notify-send"); err != nil {
			return "", nil, errors.New("notify-send isn't installed (Debian and Ubuntu: libnotify-bin)")
		}
		// notify-send's body is markup to many notification servers, and a
		// form's summary is Claude's words about a page: escape it, so a
		// page can't put a link in a notification (review 41).
		return "notify-send", []string{"--app-name=heedle", "--", title, html.EscapeString(body)}, nil
	}
	return "", nil, fmt.Errorf("no notifications on %s", goos)
}

// watch tells you, with the system's notifications, when a scheduled run
// ends and when a write waits for your approval (loom.Watcher), each
// once, until ctx ends. A notification that can't be shown is said once.
func watch(ctx context.Context, sys system, hostPort, queue, base string, out io.Writer) {
	c, err := client.NewLazyClient(client.Options{HostPort: hostPort, Logger: log.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))})
	if err != nil {
		fmt.Fprintf(out, "notifications: %v\n", err)
		return
	}
	defer c.Close()
	w := &loom.Watcher{Client: c, Queue: queue, Since: time.Now()}
	said, down := false, false
	t := time.NewTicker(sys.notifyEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		notices, err := w.Check(ctx)
		// Say once that Temporal can't be asked, and once that it can again
		// (review 33: it said nothing).
		if err != nil && !down && ctx.Err() == nil {
			fmt.Fprintf(out, "notifications: can't ask Temporal: %v\n", err)
		} else if err == nil && down {
			fmt.Fprintln(out, "notifications: Temporal answers again")
		}
		down = err != nil
		show := func(title, body string) {
			if err := sys.notify(ctx, title, body); err != nil && !said && ctx.Err() == nil {
				fmt.Fprintf(out, "notifications: %v; heedle won't show them\n", err)
				said = true
			}
		}
		for i, n := range notices {
			if i == maxShown {
				// Many at once: one more says how many, rather than a flood.
				show("heedle: "+strconv.Itoa(len(notices)-maxShown)+" more", "Scheduled runs ended or writes wait for you. "+base)
				break
			}
			show(n.Title, n.Body+" "+noticeLink(base, n))
		}
	}
}

// up starts Temporal's dev server (the temporal CLI on the PATH),
// keeping its state in the home directory, then serves the UI with a
// worker against it until ctx ends, and stops Temporal. On macOS it first
// makes sure Podman's virtual machine exists and runs, since container,
// agent and browse steps need it. Flags after -- go to the UI (drawdown's
// flags, such as -agent-image), except the ones up sets itself.
func up(ctx context.Context, args []string, out io.Writer, sys system) error {
	sys = sys.withDefaults(out)
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(out)
	home := fs.String("home", defaultHome(), "where heedle keeps its store, Temporal's database and logs")
	listen := fs.String("listen", "127.0.0.1:8090", "address to serve the UI on")
	notify := fs.Bool("notify", true, "tell you, with the system's notifications, when a scheduled run ends and when a write waits for your approval")
	port := fs.Int("temporal-port", 7233, "Temporal frontend port, on 127.0.0.1")
	uiPort := fs.Int("temporal-ui-port", 8233, "Temporal Web UI port, on 127.0.0.1")
	var uiArgs []string
	for i, a := range args {
		if a == "--" {
			args, uiArgs = args[:i], args[i+1:]
			break
		}
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (flags for the UI go after --)", fs.Arg(0))
	}
	for _, a := range uiArgs {
		name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
		if strings.HasPrefix(a, "-") && ownFlags[name] {
			return fmt.Errorf("heedle up sets -%s itself; use -home, -listen or -temporal-port instead of passing it after --", name)
		}
	}
	if *home == "" {
		return errors.New("no home directory: give -home DIR")
	}
	*home = absHome(*home)
	if err := os.MkdirAll(*home, 0o700); err != nil {
		return err
	}
	if _, err := sys.lookPath("git"); err != nil {
		return errors.New("heedle needs Git 2.42 or later on the PATH (macOS: brew install git; Linux: your package manager)")
	}
	temporal, err := sys.lookPath("temporal")
	if err != nil {
		return errors.New("heedle needs the Temporal CLI on the PATH (macOS: brew install temporal; Linux: curl -sSf https://temporal.download/cli.sh | sh)")
	}
	// A heedle that was killed may have left its Temporal running: stop
	// that one, and only that one (roadmap 9.2).
	if err := cleanStart(ctx, *home, sys, out); err != nil {
		return err
	}
	// A Temporal already on these ports would answer for ours, with
	// another database, and outlive heedle (review 21).
	hostPort := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	temporalUI := net.JoinHostPort("127.0.0.1", strconv.Itoa(*uiPort))
	for _, a := range []string{hostPort, temporalUI} {
		l, err := net.Listen("tcp", a)
		if err != nil {
			return fmt.Errorf("%s is in use: is another Temporal (temporal server start-dev) running? Stop it, or give -temporal-port and -temporal-ui-port", a)
		}
		l.Close()
	}
	podman, err := checkPodman(ctx, sys, out)
	if err != nil {
		return err
	}
	var images []string
	if podman != "" {
		images = withImages(ctx, sys, podman, uiArgs, pinned, out)
	}

	logFile, err := os.OpenFile(filepath.Join(*home, "temporal.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	targs := []string{"server", "start-dev",
		"--ip", "127.0.0.1", "--port", strconv.Itoa(*port), "--ui-port", strconv.Itoa(*uiPort),
		"--namespace", "default", "--db-filename", filepath.Join(*home, "temporal.db"), "--log-level", "warn"}
	stop, tpid, err := sys.temporal(ctx, temporal, targs, logFile, hostPort)
	if err != nil {
		return fmt.Errorf("starting Temporal (its log: %s): %w", logFile.Name(), err)
	}
	// What this heedle up started, for heedle down and the next heedle up
	// (roadmap 9.2); gone once Temporal has stopped.
	store := filepath.Join(*home, "store.git")
	st := upState{PID: os.Getpid(), Args: os.Args, Home: *home, TemporalPort: *port, TemporalUIPort: *uiPort, UI: "http://" + *listen,
		Keep: keptImages(images, uiArgs), Started: time.Now().UTC(), HomeLabel: bobbin.HomeOf(store), Pins: pinned.refs()}
	st.Temporal.PID, st.Temporal.Args = tpid, append([]string{temporal}, targs...)
	// The command line the system reports for it, which is what heedle
	// down and the next heedle up match: a temporal on the PATH that is a
	// wrapper runs the real one under another (HEEDLE-509).
	if line, err := sys.procArgs(ctx, tpid); err == nil && line != "" {
		st.Temporal.Cmdline = line
	}
	// The process that answers on the port, when it isn't the one heedle
	// started: a wrapper's child, which stopping the wrapper leaves
	// running (roadmap 9.2.1).
	if lpid, err := sys.listener(ctx, *port); err == nil && lpid != tpid {
		if line, err := sys.procArgs(ctx, lpid); err == nil && line != "" {
			st.Temporal.Listener = &process{PID: lpid, Cmdline: line}
		}
	}
	if err := writeUpState(*home, st); err != nil {
		stop()
		return err
	}
	// Other homes must see this one, or their prunes could remove its
	// images: a home that can't say it runs doesn't start (review 55).
	unregister, err := registerHome(*home, st.HomeLabel)
	if err != nil {
		stop()
		os.Remove(filepath.Join(*home, upFile))
		return fmt.Errorf("saying this home runs, in %s: %w", homesDir(), err)
	}
	defer func() {
		// A wrapper's child first: the wrapper then ends as it does.
		if ourListener(context.Background(), sys, &st, *home) {
			l := st.Temporal.Listener
			stopProcess(context.Background(), sys, l.PID, l.Cmdline, 15*time.Second, true)
		}
		stop()
		unregister()
		if answers(hostPort) {
			// Never leave without saying Temporal outlived heedle; up.json
			// stays, so heedle down can say so too.
			fmt.Fprintf(out, "Temporal still answers on %s after heedle stopped it; stop it yourself (heedle down -home %s says which process)\n", hostPort, *home)
			return
		}
		os.Remove(filepath.Join(*home, upFile))
	}()
	if podman != "" {
		sweep(ctx, hostPort, st.HomeLabel, out)
	}
	fmt.Fprintf(out, "Temporal on %s (Web UI http://%s, log %s)\n", hostPort, temporalUI, logFile.Name())
	if *notify {
		go watch(ctx, sys, hostPort, queueOf(uiArgs), "http://"+*listen, out)
	}
	// The connectors file lives in the home too (a -connectors after --
	// wins); it needn't exist until there is a connector.
	return sys.serve(ctx, append([]string{"-store", store, "-address", hostPort, "-temporal-ui", "http://" + temporalUI, "-listen", *listen,
		"-connectors", filepath.Join(*home, "connectors.json"), "-homes", homesDir(), "-keep-images", strings.Join(pinned.refs(), ",")}, append(images, uiArgs...)...), out)
}

// ownFlags are drawdown's flags heedle up sets; passing them after --
// would quietly point the UI at another store or Temporal.
var ownFlags = map[string]bool{"store": true, "address": true, "temporal-ui": true, "listen": true, "homes": true}

// TemporalReady bounds how long Temporal's dev server may take to answer.
var TemporalReady = time.Minute

// startTemporal runs Temporal's dev server and waits until its frontend
// answers a health check. It stops it with an interrupt (then a kill
// after WaitDelay) when stopped, and if it exits, ctx ends or it doesn't
// answer in time, it reports why and leaves nothing running.
func startTemporal(ctx context.Context, path string, args []string, log io.Writer, hostPort string) (func() error, int, error) {
	run, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(run, path, args...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, 0, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stop := func() error {
		cancel()
		err := <-exited
		var exit *exec.ExitError
		if errors.As(err, &exit) && !exit.Exited() {
			return nil // ended by our interrupt
		}
		return err
	}
	deadline := time.After(TemporalReady)
	for {
		c, err := client.Dial(client.Options{HostPort: hostPort, Logger: quiet{}, ConnectionOptions: client.ConnectionOptions{GetSystemInfoTimeout: time.Second}})
		if err == nil {
			c.Close()
			return stop, cmd.Process.Pid, nil
		}
		select {
		case err := <-exited:
			cancel()
			return nil, 0, fmt.Errorf("it exited: %v", err)
		case <-ctx.Done():
			stop()
			return nil, 0, ctx.Err()
		case <-deadline:
			stop()
			return nil, 0, fmt.Errorf("it didn't answer on %s within %v", hostPort, TemporalReady)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// quiet is a Temporal SDK logger that logs nothing: failed dials while
// Temporal starts are expected.
type quiet struct{}

func (quiet) Debug(string, ...any) {}
func (quiet) Info(string, ...any)  {}
func (quiet) Warn(string, ...any)  {}
func (quiet) Error(string, ...any) {}

// defaultHome is ~/.heedle, or "" without a home directory.
func defaultHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".heedle")
}

// checkPodman says what's missing without Podman (programs without
// container, agent or browse steps still run), and on macOS makes sure
// Podman's virtual machine exists and is running. It returns Podman's
// path, or "" without it.
func checkPodman(ctx context.Context, sys system, out io.Writer) (string, error) {
	podman, err := sys.lookPath("podman")
	if err != nil {
		fmt.Fprintln(out, "podman isn't on the PATH: container, agent and browse steps won't run (macOS: brew install podman; Linux: your package manager)")
		return "", nil
	}
	machine := ""
	if sys.goos == "darwin" {
		if machine, err = startMachine(ctx, sys, podman, out); err != nil {
			return "", err
		}
	}
	return podman, podmanAnswers(ctx, sys, podman, machine)
}

// podmanAnswers checks that Podman answers, not only that its machine
// says it runs: a machine left "running" after the Mac slept, or after
// its processes were killed, refuses every connection, and heedle went
// on with no images and every browse step failing (Vinod, v0.2.4).
// A machine still starting gets a minute.
func podmanAnswers(ctx context.Context, sys system, podman, machine string) error {
	var b []byte
	var err error
	for try := 0; try < 30; try++ {
		if b, err = sys.combined(ctx, podman, "info", "--format", "{{.Host.Arch}}"); err == nil || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(sys.podmanRetry):
		}
	}
	if err == nil {
		return nil
	}
	why := reason(b, err)
	if machine == "" {
		if sys.goos == "darwin" {
			return fmt.Errorf("podman doesn't answer (%s): restart its virtual machine with podman machine stop, then podman machine start, and run heedle up again", why)
		}
		return fmt.Errorf("podman doesn't answer (%s): check podman info works, then run heedle up again", why)
	}
	return fmt.Errorf("podman doesn't answer, though its virtual machine %s says it runs (%s): restart it with podman machine stop %s, then podman machine start %s, and run heedle up again", machine, why, machine, machine)
}

// startMachine makes sure Podman's virtual machine exists and runs, and
// returns its name ("" for one it just made, which is the default).
func startMachine(ctx context.Context, sys system, podman string, out io.Writer) (string, error) {
	b, err := sys.output(ctx, podman, "machine", "list", "--format", "json")
	if err != nil {
		return "", fmt.Errorf("podman machine list: %w", err)
	}
	var machines []struct {
		Name             string
		Default, Running bool
		Starting         bool
	}
	if err := json.Unmarshal(b, &machines); err != nil {
		return "", fmt.Errorf("podman machine list: %w", err)
	}
	if len(machines) == 0 {
		fmt.Fprintln(out, "Creating Podman's virtual machine (podman machine init; this takes a few minutes, once)…")
		if _, err := sys.output(ctx, podman, "machine", "init"); err != nil {
			return "", fmt.Errorf("podman machine init: %w (if it left a machine half made, remove it with podman machine rm, then run heedle up again)", err)
		}
		fmt.Fprintln(out, "Starting Podman's virtual machine (podman machine start)…")
		if _, err := sys.output(ctx, podman, "machine", "start"); err != nil {
			return "", fmt.Errorf("podman machine start: %w", err)
		}
		return "", nil
	}
	// Podman runs one machine at a time; one already running or starting
	// is used as it is (review 21).
	name := machines[0].Name
	for _, m := range machines {
		if m.Running || m.Starting {
			return m.Name, nil
		}
		if m.Default {
			name = m.Name
		}
	}
	fmt.Fprintf(out, "Starting Podman's virtual machine %s (podman machine start)…\n", name)
	if _, err := sys.output(ctx, podman, "machine", "start", name); err != nil {
		return "", fmt.Errorf("podman machine start %s: %w", name, err)
	}
	return name, nil
}

// maxShown bounds the notifications shown at once; one more says how
// many others there are.
const maxShown = 5

// queueOf is the task queue the UI's flags name (-queue), else loom's.
func queueOf(uiArgs []string) string {
	for i, a := range uiArgs {
		switch {
		case (a == "-queue" || a == "--queue") && i+1 < len(uiArgs):
			return uiArgs[i+1]
		case strings.HasPrefix(a, "-queue=") || strings.HasPrefix(a, "--queue="):
			return a[strings.Index(a, "=")+1:]
		}
	}
	return "loom"
}

// noticeLink is where a notice's link goes: a run's page, or, for a
// notice about a schedule itself (its budget paused it: roadmap 10.4),
// the Schedules page.
func noticeLink(base string, n loom.Notice) string {
	if n.Schedule != "" {
		return base + "/#/schedules"
	}
	return base + "/#/run/" + url.PathEscape(n.Workflow) + "/" + url.PathEscape(n.Run)
}
