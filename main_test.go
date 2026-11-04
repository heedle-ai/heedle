package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var ctx = context.Background()

// TestInv_HeedleIsLoomAndDrawdown: heedle runs every loom command
// unchanged (cli.Run), serves drawdown (app.Run) as heedle ui, and says
// its version; nothing is reimplemented.
func TestInv_HeedleIsLoomAndDrawdown(t *testing.T) {
	var out bytes.Buffer
	if err := run(ctx, []string{"compile", `(pipe (echo "a") echo)`}, &out); err != nil || !strings.Contains(out.String(), `"requires": [`) {
		t.Errorf("heedle compile: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := run(ctx, []string{"version"}, &out); err != nil || out.String() != "heedle dev\n" {
		t.Errorf("heedle version: %q %v", out.String(), err)
	}
	if err := run(ctx, []string{"nonsense"}, io.Discard); err == nil || !strings.Contains(err.Error(), "nonsense") {
		t.Errorf("an unknown command: %v", err)
	}
	if err := run(ctx, []string{"ui"}, io.Discard); err == nil || !strings.Contains(err.Error(), "-store") {
		t.Errorf("heedle ui is drawdown's: %v", err)
	}
	var help bytes.Buffer
	if err := run(ctx, []string{"up", "-h"}, &help); !errors.Is(err, flag.ErrHelp) || !strings.Contains(help.String(), "-temporal-port") {
		t.Errorf("heedle up -h: %v\n%s", err, help.String())
	}
	if err := run(ctx, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "heedle up") {
		t.Errorf("no command: %v", err)
	}
}

// machine is a fake system: the programs it has, and what it was asked.
type machine struct {
	goos      string
	has       map[string]bool
	machines  string // podman machine list's output
	mu        sync.Mutex
	calls     []string
	started   []string // temporal's path and arguments
	hostPort  string
	stopped   int
	served    []string
	failServe error
}

func (m *machine) system() system {
	return system{
		goos: m.goos,
		lookPath: func(name string) (string, error) {
			if m.has[name] {
				return "/usr/local/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		output: func(_ context.Context, name string, args ...string) ([]byte, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.calls = append(m.calls, filepath.Base(name)+" "+strings.Join(args, " "))
			if strings.Join(args, " ") == "machine list --format json" {
				return []byte(m.machines), nil
			}
			return nil, nil
		},
		temporal: func(_ context.Context, path string, args []string, _ io.Writer, hostPort string) (func() error, error) {
			m.started, m.hostPort = append([]string{path}, args...), hostPort
			return func() error { m.stopped++; return nil }, nil
		},
		serve: func(_ context.Context, args []string, _ io.Writer) error {
			if m.started == nil || m.stopped > 0 {
				return errors.New("the UI started without Temporal running")
			}
			m.served = args
			return m.failServe
		},
	}
}

// TestInv_UpStartsTemporalThenTheUI: heedle up starts the temporal on
// the PATH (server start-dev, on 127.0.0.1, with its database in the home
// directory), then serves the UI and a worker against it with the home's
// store and any flags after --, and stops Temporal once the UI stops,
// even when it fails. It refuses before starting anything without Git or
// the Temporal CLI, when Temporal's ports are taken (another Temporal
// would answer for ours), or when a flag after -- would override the
// store, address or listen address it sets. (Review 21.)
func TestInv_UpStartsTemporalThenTheUI(t *testing.T) {
	home := t.TempDir()
	port, uiPort := freePort(t), freePort(t)
	ports := []string{"-temporal-port", fmt.Sprint(port), "-temporal-ui-port", fmt.Sprint(uiPort)}
	m := &machine{goos: "linux", has: map[string]bool{"git": true, "podman": true, "temporal": true}}
	var out bytes.Buffer
	err := up(ctx, append(append([]string{"-home", home, "-listen", "127.0.0.1:9000"}, ports...), "--", "-agent-image", "abc"), &out, m.system())
	if err != nil {
		t.Fatal(err)
	}
	address := fmt.Sprintf("127.0.0.1:%d", port)
	wantStart := fmt.Sprintf("/usr/local/bin/temporal server start-dev --ip 127.0.0.1 --port %d --ui-port %d --namespace default --db-filename %s --log-level warn", port, uiPort, filepath.Join(home, "temporal.db"))
	if strings.Join(m.started, " ") != wantStart || m.hostPort != address {
		t.Errorf("Temporal started as %q at %s", m.started, m.hostPort)
	}
	want := []string{"-store", filepath.Join(home, "store.git"), "-address", address, "-temporal-ui", fmt.Sprintf("http://127.0.0.1:%d", uiPort), "-listen", "127.0.0.1:9000", "-connectors", filepath.Join(home, "connectors.json"), "-agent-image", "abc"}
	if strings.Join(m.served, " ") != strings.Join(want, " ") || m.stopped != 1 {
		t.Errorf("served %q, Temporal stopped %d times", m.served, m.stopped)
	}
	if len(m.calls) != 0 {
		t.Errorf("on Linux, heedle up ran %q", m.calls)
	}

	m = &machine{goos: "linux", has: map[string]bool{"git": true, "temporal": true}, failServe: errors.New("address in use")}
	out.Reset()
	if err := up(ctx, append([]string{"-home", home}, ports...), &out, m.system()); err == nil || m.stopped != 1 || !strings.Contains(out.String(), "podman isn't on the PATH") {
		t.Errorf("a UI that fails: %v, Temporal stopped %d times\n%s", err, m.stopped, out.String())
	}

	busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", uiPort))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	for _, c := range []struct {
		has  map[string]bool
		args []string
		want string
	}{
		{map[string]bool{"temporal": true}, nil, "Git"},
		{map[string]bool{"git": true}, nil, "Temporal CLI"},
		{map[string]bool{"git": true, "temporal": true}, nil, "in use"},
		{map[string]bool{"git": true, "temporal": true}, []string{"stray"}, "unexpected"},
		{map[string]bool{"git": true, "temporal": true}, []string{"--", "-store", "/elsewhere"}, "sets -store"},
		{map[string]bool{"git": true, "temporal": true}, []string{"--", "--address=x:1"}, "sets -address"},
	} {
		m := &machine{goos: "linux", has: c.has}
		err := up(ctx, append(append([]string{"-home", home}, ports...), c.args...), io.Discard, m.system())
		if err == nil || !strings.Contains(err.Error(), c.want) || m.started != nil {
			t.Errorf("%v %q: %v, Temporal started: %v", c.has, c.args, err, m.started != nil)
		}
	}
}

// TestInv_UpPreparesPodmanOnMacOnly: on macOS, heedle up creates and
// starts Podman's virtual machine if there is none, starts the default
// one (or the only one) if none is running, and leaves one that is
// running or starting alone; elsewhere it never touches Podman machines.
// (Review 21: a starting machine, and a machine not named
// podman-machine-default.)
func TestInv_UpPreparesPodmanOnMacOnly(t *testing.T) {
	for _, c := range []struct {
		goos, machines, want string
	}{
		{"darwin", `[]`, "podman machine list --format json|podman machine init|podman machine start"},
		{"darwin", `[{"Name":"podman-machine-default","Default":true,"Running":false}]`, "podman machine list --format json|podman machine start podman-machine-default"},
		{"darwin", `[{"Name":"mine","Running":false}]`, "podman machine list --format json|podman machine start mine"},
		{"darwin", `[{"Name":"a"},{"Name":"b","Default":true}]`, "podman machine list --format json|podman machine start b"},
		{"darwin", `[{"Name":"other"},{"Name":"podman-machine-default","Default":true,"Running":true}]`, "podman machine list --format json"},
		{"darwin", `[{"Name":"podman-machine-default","Default":true,"Starting":true}]`, "podman machine list --format json"},
		{"linux", `[]`, ""},
	} {
		m := &machine{goos: c.goos, machines: c.machines, has: map[string]bool{"git": true, "podman": true, "temporal": true}}
		if err := up(ctx, []string{"-home", t.TempDir(), "-temporal-port", fmt.Sprint(freePort(t)), "-temporal-ui-port", fmt.Sprint(freePort(t))}, io.Discard, m.system()); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(m.calls, "|"); got != c.want {
			t.Errorf("%s with %s: ran %q, want %q", c.goos, c.machines, got, c.want)
		}
	}
	bad := &machine{goos: "darwin", machines: "not json", has: map[string]bool{"git": true, "podman": true, "temporal": true}}
	if err := up(ctx, []string{"-home", t.TempDir(), "-temporal-port", fmt.Sprint(freePort(t)), "-temporal-ui-port", fmt.Sprint(freePort(t))}, io.Discard, bad.system()); err == nil || bad.started != nil {
		t.Errorf("an unreadable machine list: %v", err)
	}
}

// fakeTemporal is a temporal CLI that runs script and writes its PID to
// the file it returns.
func fakeTemporal(t *testing.T, script string) (string, string) {
	dir := t.TempDir()
	pid := filepath.Join(dir, "pid")
	p := filepath.Join(dir, "temporal")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho $$ > "+pid+"\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p, pid
}

// alive reports whether the process whose PID is in file still runs.
func alive(t *testing.T, file string) bool {
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	fmt.Sscan(string(b), &pid)
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}

// TestInv_TemporalNeverOutlivesUp: a Temporal that exits while starting
// is reported at once, with what it logged; one that doesn't answer in
// time, or is still starting when heedle is stopped, is stopped, so
// nothing is left running. (Review 21: the SDK's launcher waited a minute
// for a dead server, and left a starting one running.)
func TestInv_TemporalNeverOutlivesUp(t *testing.T) {
	address := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	dead, _ := fakeTemporal(t, "echo cannot open database >&2; exit 1")
	var log bytes.Buffer
	start := time.Now()
	if _, err := startTemporal(ctx, dead, nil, &log, address); err == nil || !strings.Contains(err.Error(), "exited") || time.Since(start) > 10*time.Second || !strings.Contains(log.String(), "cannot open database") {
		t.Errorf("a Temporal that exits: %v after %v, logged %q", err, time.Since(start), log.String())
	}

	slow, pid := fakeTemporal(t, "exec sleep 1000")
	c, cancel := context.WithCancel(ctx)
	time.AfterFunc(time.Second, cancel)
	if _, err := startTemporal(c, slow, nil, io.Discard, address); !errors.Is(err, context.Canceled) || alive(t, pid) {
		t.Errorf("stopped while starting: %v, still running: %v", err, alive(t, pid))
	}

	defer func(d time.Duration) { TemporalReady = d }(TemporalReady)
	TemporalReady = time.Second
	slow, pid = fakeTemporal(t, "exec sleep 1000")
	if _, err := startTemporal(ctx, slow, nil, io.Discard, address); err == nil || !strings.Contains(err.Error(), "didn't answer") || alive(t, pid) {
		t.Errorf("a Temporal that never answers: %v, still running: %v", err, alive(t, pid))
	}
}

// syncBuffer is a bytes.Buffer safe to write and read at once.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestE2E_Up runs heedle up for real (the temporal CLI on the PATH),
// submits a program
// through the UI's API, and waits for the worker to run and record it;
// then stops everything, Temporal included. CI's e2e step runs it with HEEDLE_E2E=1.
func TestE2E_Up(t *testing.T) {
	if os.Getenv("HEEDLE_E2E") != "1" {
		t.Skip("set HEEDLE_E2E=1 to run heedle up against a real Temporal server")
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	address := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	home := t.TempDir()
	var out syncBuffer
	done := make(chan error, 1)
	uiPort := fmt.Sprint(freePort(t))
	// Notifications, recorded instead of shown (roadmap 8.5).
	var notes syncBuffer
	sys := system{notifyEvery: time.Second, notify: func(_ context.Context, title, body string) error {
		fmt.Fprintf(&notes, "%s | %s\n", title, body)
		return nil
	}}
	go func() {
		done <- up(c, []string{"-home", home, "-listen", listen,
			"-temporal-port", strings.TrimPrefix(address, "127.0.0.1:"), "-temporal-ui-port", uiPort}, &out, sys)
	}()
	base := "http://" + listen
	for !strings.Contains(out.String(), "drawdown on") {
		select {
		case err := <-done:
			t.Fatalf("heedle up stopped: %v\n%s", err, out.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	post := func(path, body string) map[string]any {
		req, _ := http.NewRequestWithContext(c, "POST", base+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", base)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v map[string]any
		json.NewDecoder(res.Body).Decode(&v)
		if res.StatusCode/100 != 2 {
			t.Fatalf("POST %s: %d %v", path, res.StatusCode, v)
		}
		return v
	}
	started := post("/api/runs", `{"program":"(pipe (par (echo \"a\") (echo \"b\")) echo)"}`)
	wf, _ := started["workflowId"].(string)
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for {
		res, err := http.Get(base + "/api/runs")
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			Runs []struct {
				WorkflowID string `json:"workflowId"`
				Status     string `json:"status"`
				Commit     string `json:"commit"`
			} `json:"runs"`
		}
		json.NewDecoder(res.Body).Decode(&v)
		res.Body.Close()
		ran := false
		for _, r := range v.Runs {
			ran = ran || r.WorkflowID == wf && r.Status == "Completed" && commit.MatchString(r.Commit)
		}
		if ran {
			break
		}
		select {
		case <-c.Done():
			t.Fatalf("the run never completed: %+v\n%s", v.Runs, out.String())
		case <-time.After(500 * time.Millisecond):
		}
	}
	// A schedule, on a real Temporal: made from the UI, run now, its run
	// recorded like any other, then deleted (roadmap 7.3).
	post("/api/schedules", `{"name":"e2e-digest","program":"(pipe (echo \"scheduled\") echo)","every":"24h"}`)
	post("/api/schedules/e2e-digest/run", `{}`)
	scheduled := ""
	for scheduled == "" {
		var v struct {
			Schedules []struct {
				Name   string `json:"name"`
				Recent []struct {
					WorkflowID string `json:"workflowId"`
				} `json:"recent"`
			} `json:"schedules"`
			Runs []struct {
				WorkflowID string `json:"workflowId"`
				Status     string `json:"status"`
				Commit     string `json:"commit"`
			} `json:"runs"`
		}
		for _, path := range []string{"/api/schedules", "/api/runs"} {
			res, err := http.Get(base + path)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			res.Body.Close()
		}
		for _, s := range v.Schedules {
			for _, a := range s.Recent {
				for _, r := range v.Runs {
					if s.Name == "e2e-digest" && r.WorkflowID == a.WorkflowID && r.Status == "Completed" && commit.MatchString(r.Commit) {
						scheduled = r.WorkflowID
					}
				}
			}
		}
		if scheduled != "" {
			break
		}
		select {
		case <-c.Done():
			t.Fatalf("the scheduled run never completed: %+v\n%s", v, out.String())
		case <-time.After(500 * time.Millisecond):
		}
	}
	// heedle up tells you the scheduled run ended, once, with a link to it;
	// the run started by hand isn't news.
	for !strings.Contains(notes.String(), "heedle: e2e-digest finished") {
		select {
		case <-c.Done():
			t.Fatalf("no notification of the scheduled run: %q", notes.String())
		case <-time.After(500 * time.Millisecond):
		}
	}
	time.Sleep(3 * time.Second) // a few more looks
	if n := strings.Count(notes.String(), "e2e-digest finished"); n != 1 || !strings.Contains(notes.String(), base+"/#/run/"+url.PathEscape(scheduled)+"/") || strings.Count(notes.String(), "\n") != 1 {
		t.Errorf("notifications: %q", notes.String())
	}
	var sched bytes.Buffer
	if err := run(c, []string{"schedules", "-address", address, "-delete", "e2e-digest"}, &sched); err != nil {
		t.Errorf("heedle schedules -delete: %v\n%s", err, sched.String())
	}
	// Temporal's list of schedules catches up in a moment (review 30).
	for !strings.Contains(sched.String(), "no schedules") {
		select {
		case <-c.Done():
			t.Fatalf("the deleted schedule is still listed:\n%s", sched.String())
		case <-time.After(time.Second):
		}
		sched.Reset()
		if err := run(c, []string{"schedules", "-address", address}, &sched); err != nil {
			t.Fatalf("heedle schedules: %v", err)
		}
	}
	// The terminal commands, against the same Temporal and store.
	var cli bytes.Buffer
	if err := run(c, []string{"run", "-address", address, `(pipe (echo "from the terminal") echo)`}, &cli); err != nil || !strings.Contains(cli.String(), "from the terminal") {
		t.Errorf("heedle run: %v\n%s", err, cli.String())
	}
	cli.Reset()
	if err := run(c, []string{"beliefs", "-store", filepath.Join(home, "store.git")}, &cli); err != nil {
		t.Errorf("heedle beliefs: %v\n%s", err, cli.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("heedle up after it was stopped: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("heedle up kept running after it was stopped")
	}
	if conn, err := net.Dial("tcp", address); err == nil {
		conn.Close()
		t.Error("Temporal still answers after heedle up stopped")
	}
}

// TestInv_NotificationsAreTheSystems: heedle up shows notifications with
// the system's own tool, the title and body as its arguments, never in a
// script (osascript on macOS, notify-send on Linux, which it says how to
// install if it's missing); it watches the queue the UI is given.
// (Roadmap 8.5.)
func TestInv_NotificationsAreTheSystems(t *testing.T) {
	evil := `x" & (do shell script "touch /tmp/pwned") & "`
	name, args, err := notifyCommand("darwin", nil, "-heedle: done", evil)
	// After "--", so a title starting with "-" is never an option (review 33).
	if err != nil || name != "osascript" || args[len(args)-1] != evil || args[len(args)-2] != "-heedle: done" || args[len(args)-3] != "--" || strings.Contains(strings.Join(args[:len(args)-2], " "), evil) {
		t.Errorf("macOS: %s %q %v", name, args, err)
	}
	found := func(string) (string, error) { return "/usr/bin/notify-send", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	if name, args, err := notifyCommand("linux", found, "-t", "body"); err != nil || name != "notify-send" || strings.Join(args, " ") != "--app-name=heedle -- -t body" {
		t.Errorf("Linux: %s %q %v", name, args, err)
	}
	if _, _, err := notifyCommand("linux", missing, "t", "b"); err == nil || !strings.Contains(err.Error(), "libnotify-bin") {
		t.Errorf("Linux without notify-send: %v", err)
	}
	if _, _, err := notifyCommand("windows", found, "t", "b"); err == nil {
		t.Error("windows")
	}
	for args, want := range map[string]string{"": "loom", "-queue research": "research", "--queue=x": "x", "-agent-image i": "loom"} {
		if got := queueOf(strings.Fields(args)); got != want {
			t.Errorf("queueOf(%q) = %q", args, got)
		}
	}
}
