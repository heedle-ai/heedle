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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"

	"github.com/heedle-ai/drawdown/app"
)

// system is what heedle up uses from the machine: programs on the PATH,
// Temporal's dev server and the UI. Its zero value is the real machine;
// tests replace parts.
type system struct {
	goos     string
	lookPath func(string) (string, error)
	// output runs a program and returns its standard output.
	output func(ctx context.Context, name string, args ...string) ([]byte, error)
	// temporal starts Temporal's dev server and returns its frontend
	// address and a function that stops it.
	temporal func(ctx context.Context, o testsuite.DevServerOptions) (string, func() error, error)
	// serve serves the UI and a worker until ctx ends (drawdown's app.Run).
	serve func(ctx context.Context, args []string, out io.Writer) error
}

func (s system) withDefaults(out io.Writer) system {
	if s.goos == "" {
		s.goos = runtime.GOOS
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
	if s.temporal == nil {
		s.temporal = func(ctx context.Context, o testsuite.DevServerOptions) (string, func() error, error) {
			srv, err := testsuite.StartDevServer(ctx, o)
			if err != nil {
				return "", nil, err
			}
			return srv.FrontendHostPort(), srv.Stop, nil
		}
	}
	if s.serve == nil {
		s.serve = app.Run
	}
	return s
}

// up starts Temporal's dev server (the temporal CLI on the PATH, or the
// one Temporal's Go SDK downloads), keeping its state in the home
// directory, then serves the UI with a worker against it until ctx ends,
// and stops Temporal. On macOS it first makes sure Podman's virtual
// machine exists and runs, since container, agent and browse steps need
// it. Flags after -- go to the UI (drawdown's flags, such as
// -agent-image).
func up(ctx context.Context, args []string, out io.Writer, sys system) error {
	sys = sys.withDefaults(out)
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(out)
	home := fs.String("home", defaultHome(), "where heedle keeps its store, Temporal's database and logs")
	listen := fs.String("listen", "127.0.0.1:8090", "address to serve the UI on")
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
	if *home == "" {
		return errors.New("no home directory: give -home DIR")
	}
	if err := os.MkdirAll(*home, 0o700); err != nil {
		return err
	}
	if _, err := sys.lookPath("git"); err != nil {
		return errors.New("heedle needs Git 2.42 or later on the PATH (macOS: brew install git; Linux: your package manager)")
	}
	if err := checkPodman(ctx, sys, out); err != nil {
		return err
	}

	opts := testsuite.DevServerOptions{
		ClientOptions:  &client.Options{HostPort: net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)), Namespace: "default"},
		CachedDownload: testsuite.CachedDownload{DestDir: filepath.Join(*home, "bin")},
		DBFilename:     filepath.Join(*home, "temporal.db"),
		EnableUI:       true,
		UIPort:         strconv.Itoa(*uiPort),
		LogLevel:       "warn",
	}
	if path, err := sys.lookPath("temporal"); err == nil {
		opts.ExistingPath = path
	} else {
		fmt.Fprintf(out, "temporal isn't on the PATH; using the one Temporal's SDK downloads to %s\n", opts.CachedDownload.DestDir)
	}
	logFile, err := os.OpenFile(filepath.Join(*home, "temporal.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	opts.Stdout, opts.Stderr = logFile, logFile
	hostPort, stop, err := sys.temporal(ctx, opts)
	if err != nil {
		return fmt.Errorf("starting Temporal (its log: %s): %w", logFile.Name(), err)
	}
	defer stop()
	temporalUI := "http://127.0.0.1:" + opts.UIPort
	fmt.Fprintf(out, "Temporal on %s (Web UI %s, log %s)\n", hostPort, temporalUI, logFile.Name())
	return sys.serve(ctx, append([]string{"-store", filepath.Join(*home, "store.git"), "-address", hostPort, "-temporal-ui", temporalUI, "-listen", *listen}, uiArgs...), out)
}

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
// Podman's virtual machine exists and is running.
func checkPodman(ctx context.Context, sys system, out io.Writer) error {
	podman, err := sys.lookPath("podman")
	if err != nil {
		fmt.Fprintln(out, "podman isn't on the PATH: container, agent and browse steps won't run (macOS: brew install podman; Linux: your package manager)")
		return nil
	}
	if sys.goos != "darwin" {
		return nil
	}
	b, err := sys.output(ctx, podman, "machine", "list", "--format", "json")
	if err != nil {
		return fmt.Errorf("podman machine list: %w", err)
	}
	var machines []struct {
		Name    string
		Running bool
	}
	if err := json.Unmarshal(b, &machines); err != nil {
		return fmt.Errorf("podman machine list: %w", err)
	}
	if len(machines) == 0 {
		fmt.Fprintln(out, "Creating Podman's virtual machine (podman machine init; this takes a few minutes, once)…")
		if _, err := sys.output(ctx, podman, "machine", "init"); err != nil {
			return fmt.Errorf("podman machine init: %w", err)
		}
	}
	for _, m := range machines {
		if m.Running {
			return nil
		}
	}
	fmt.Fprintln(out, "Starting Podman's virtual machine (podman machine start)…")
	if _, err := sys.output(ctx, podman, "machine", "start"); err != nil {
		return fmt.Errorf("podman machine start: %w", err)
	}
	return nil
}
