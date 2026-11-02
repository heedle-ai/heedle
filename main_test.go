package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
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
	opts      *testsuite.DevServerOptions
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
		temporal: func(_ context.Context, o testsuite.DevServerOptions) (string, func() error, error) {
			m.opts = &o
			return o.ClientOptions.HostPort, func() error { m.stopped++; return nil }, nil
		},
		serve: func(_ context.Context, args []string, _ io.Writer) error {
			if m.opts == nil || m.stopped > 0 {
				return errors.New("the UI started without Temporal running")
			}
			m.served = args
			return m.failServe
		},
	}
}

// TestInv_UpStartsTemporalThenTheUI: heedle up starts Temporal's dev
// server (the temporal on the PATH, or the SDK's download, into the home
// directory), keeping its database in the home directory with its Web
// UI on, then serves the UI and a worker against it with the home's
// store and any flags after --, and stops Temporal once the UI stops,
// even when it fails. Without Git it refuses before starting anything.
func TestInv_UpStartsTemporalThenTheUI(t *testing.T) {
	home := t.TempDir()
	m := &machine{goos: "linux", has: map[string]bool{"git": true, "podman": true, "temporal": true}}
	var out bytes.Buffer
	err := up(ctx, []string{"-home", home, "-listen", "127.0.0.1:9000", "--", "-agent-image", "abc"}, &out, m.system())
	if err != nil {
		t.Fatal(err)
	}
	o := m.opts
	if o.ExistingPath != "/usr/local/bin/temporal" || o.DBFilename != filepath.Join(home, "temporal.db") || !o.EnableUI || o.UIPort != "8233" || o.ClientOptions.HostPort != "127.0.0.1:7233" {
		t.Errorf("Temporal started with %+v", o)
	}
	want := []string{"-store", filepath.Join(home, "store.git"), "-address", "127.0.0.1:7233", "-temporal-ui", "http://127.0.0.1:8233", "-listen", "127.0.0.1:9000", "-agent-image", "abc"}
	if strings.Join(m.served, " ") != strings.Join(want, " ") || m.stopped != 1 {
		t.Errorf("served %q, Temporal stopped %d times", m.served, m.stopped)
	}
	if len(m.calls) != 0 {
		t.Errorf("on Linux, heedle up ran %q", m.calls)
	}

	m = &machine{goos: "linux", has: map[string]bool{"git": true}, failServe: errors.New("address in use")}
	out.Reset()
	if err := up(ctx, []string{"-home", home}, &out, m.system()); err == nil || m.stopped != 1 {
		t.Errorf("a UI that fails: %v, Temporal stopped %d times", err, m.stopped)
	}
	if m.opts.ExistingPath != "" || m.opts.CachedDownload.DestDir != filepath.Join(home, "bin") || !strings.Contains(out.String(), "podman isn't on the PATH") {
		t.Errorf("without temporal and podman: %+v\n%s", m.opts, out.String())
	}

	m = &machine{goos: "linux", has: map[string]bool{"temporal": true}}
	if err := up(ctx, []string{"-home", home}, io.Discard, m.system()); err == nil || !strings.Contains(err.Error(), "Git") || m.opts != nil {
		t.Errorf("without git: %v, Temporal started: %v", err, m.opts != nil)
	}
	if err := up(ctx, []string{"-home", home, "stray"}, io.Discard, (&machine{has: map[string]bool{"git": true}}).system()); err == nil {
		t.Error("a stray argument was accepted")
	}
}

// TestInv_UpPreparesPodmanOnMacOnly: on macOS, heedle up creates
// Podman's virtual machine if there is none and starts it if it isn't
// running, and leaves a running one alone; elsewhere it never touches
// Podman machines.
func TestInv_UpPreparesPodmanOnMacOnly(t *testing.T) {
	for _, c := range []struct {
		goos, machines, want string
	}{
		{"darwin", `[]`, "podman machine list --format json|podman machine init|podman machine start"},
		{"darwin", `[{"Name":"podman-machine-default*","Running":false}]`, "podman machine list --format json|podman machine start"},
		{"darwin", `[{"Name":"other","Running":false},{"Name":"podman-machine-default*","Running":true}]`, "podman machine list --format json"},
		{"linux", `[]`, ""},
	} {
		m := &machine{goos: c.goos, machines: c.machines, has: map[string]bool{"git": true, "podman": true}}
		if err := up(ctx, []string{"-home", t.TempDir()}, io.Discard, m.system()); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(m.calls, "|"); got != c.want {
			t.Errorf("%s with %s: ran %q, want %q", c.goos, c.machines, got, c.want)
		}
	}
	bad := &machine{goos: "darwin", machines: "not json", has: map[string]bool{"git": true, "podman": true}}
	if err := up(ctx, []string{"-home", t.TempDir()}, io.Discard, bad.system()); err == nil || bad.opts != nil {
		t.Errorf("an unreadable machine list: %v", err)
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

// TestE2E_Up runs heedle up for real (Temporal's dev server, which the
// SDK downloads when temporal isn't on the PATH), submits a program
// through the UI's API, and waits for the worker to run and record it;
// then stops everything. CI's e2e step runs it with HEEDLE_E2E=1.
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
	go func() {
		done <- up(c, []string{"-home", home, "-listen", listen,
			"-temporal-port", strings.TrimPrefix(address, "127.0.0.1:"), "-temporal-ui-port", fmt.Sprint(freePort(t))}, &out, system{})
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
}
