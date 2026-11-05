package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for a process heedle may stop:
// with HEEDLE_STANDIN=1 it waits for an interrupt (as heedle and
// Temporal do), or, with HEEDLE_STANDIN_STUBBORN=1, ignores it.
func TestMain(m *testing.M) {
	if os.Getenv("HEEDLE_STANDIN") == "1" {
		if os.Getenv("HEEDLE_STANDIN_STUBBORN") == "1" {
			signal.Ignore(os.Interrupt)
		} else {
			c := make(chan os.Signal, 1)
			signal.Notify(c, os.Interrupt)
			go func() { <-c; os.Exit(0) }()
		}
		time.Sleep(5 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// standIn starts the test binary as a stand-in with the given arguments
// after its own: its command line is then exactly what up.json records.
func standIn(t *testing.T, stubborn bool, args ...string) (int, []string) {
	t.Helper()
	self, _ := os.Executable()
	argv := append([]string{self, "-test.run=^$", "--"}, args...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "HEEDLE_STANDIN=1")
	if stubborn {
		cmd.Env = append(cmd.Env, "HEEDLE_STANDIN_STUBBORN=1")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	t.Cleanup(func() { cmd.Process.Kill() })
	// Wait until the system reports its command line.
	sys := system{goos: goos()}.withDefaults(io.Discard)
	for i := 0; i < 100; i++ {
		if got, _ := sys.procArgs(ctx, cmd.Process.Pid); got == strings.Join(argv, " ") {
			return cmd.Process.Pid, argv
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stand-in %d never showed its command line", cmd.Process.Pid)
	return 0, nil
}

func goos() string {
	if _, err := os.Stat("/proc/self/cmdline"); err == nil {
		return "linux"
	}
	return "darwin"
}

// running reports whether pid is alive, giving one just stopped a moment
// to be reaped (a killed stand-in is a zombie until the test reaps it).
func running(pid int) bool {
	for i := 0; i < 50; i++ {
		p, err := os.FindProcess(pid)
		if err != nil || p.Signal(syscall.Signal(0)) != nil {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// temporalArgs are a dev server's arguments with home's database.
func temporalArgs(home string) []string {
	return []string{"server", "start-dev", "--port", "7233", "--db-filename", filepath.Join(home, "temporal.db")}
}

// TestInv_UpRecordsWhatItStarted: heedle up writes up.json in its home:
// its own process and command line, Temporal's process and command line
// with this home's database, the ports, the UI's address and the images
// its worker was given; and removes it once Temporal has stopped
// (HEEDLE-500).
func TestInv_UpRecordsWhatItStarted(t *testing.T) {
	home := t.TempDir()
	port, uiPort := freePort(t), freePort(t)
	m := &machine{goos: "linux", has: map[string]bool{"git": true, "temporal": true}}
	sys := m.system()
	var seen *upState
	serve := sys.serve
	sys.serve = func(c2 context.Context, args []string, out io.Writer) error {
		st, err := readUpState(home)
		if err != nil {
			return err
		}
		seen = st
		return serve(c2, args, out)
	}
	sys.temporal = func(_ context.Context, path string, args []string, _ io.Writer, _ string) (func() error, int, error) {
		m.started = append([]string{path}, args...)
		return func() error {
			if _, err := os.Stat(filepath.Join(home, upFile)); err != nil {
				t.Error("up.json was gone before Temporal stopped")
			}
			m.stopped++
			return nil
		}, 4242, nil
	}
	if err := up(ctx, []string{"-home", home, "-listen", "127.0.0.1:9000", "-temporal-port", fmt.Sprint(port), "-temporal-ui-port", fmt.Sprint(uiPort), "--", "-agent-image", "mine"}, io.Discard, sys); err != nil {
		t.Fatal(err)
	}
	if seen == nil || seen.PID != os.Getpid() || strings.Join(seen.Args, " ") != strings.Join(os.Args, " ") || seen.Home != home ||
		seen.Temporal.PID != 4242 || strings.Join(seen.Temporal.Args, " ") != strings.Join(m.started, " ") ||
		seen.TemporalPort != port || seen.TemporalUIPort != uiPort || seen.UI != "http://127.0.0.1:9000" || strings.Join(seen.Keep, " ") != "mine" {
		t.Errorf("up.json while serving: %+v", seen)
	}
	if !strings.Contains(strings.Join(seen.Temporal.Args, " "), "--db-filename "+filepath.Join(home, "temporal.db")) {
		t.Errorf("up.json's Temporal hasn't this home's database: %q", seen.Temporal.Args)
	}
	if _, err := os.Stat(filepath.Join(home, upFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("up.json outlived heedle up: %v", err)
	}
}

// TestInv_OnlyHeedlesOwnProcessesAreStopped: heedle down and the clean
// start stop a process only if it still runs exactly the command line
// up.json records, and for Temporal only one with this home's database:
// a decoy under the recorded process ID, a Temporal with another home's
// database, one on the port that heedle didn't start, and a process ID
// that's gone all survive or are left alone (HEEDLE-501).
func TestInv_OnlyHeedlesOwnProcessesAreStopped(t *testing.T) {
	sys := system{goos: goos(), lookPath: func(string) (string, error) { return "", errors.New("no podman here") }}.withDefaults(io.Discard)
	home := t.TempDir()
	write := func(st upState) {
		st.Home = home
		if err := writeUpState(home, st); err != nil {
			t.Fatal(err)
		}
	}
	// A decoy with the recorded process ID but another command line.
	decoy, _ := standIn(t, false, "something-else")
	other := t.TempDir()
	// A Temporal with another home's database.
	elsewhere, elsewhereArgs := standIn(t, false, temporalArgs(other)...)
	st := upState{PID: decoy, Args: []string{"/usr/local/bin/heedle", "up"}, UI: "http://127.0.0.1:8090"}
	st.Temporal.PID, st.Temporal.Args = elsewhere, elsewhereArgs
	write(st)
	var out bytes.Buffer
	if err := down(ctx, []string{"-home", home}, &out, sys); err != nil {
		t.Fatal(err)
	}
	if !running(decoy) || !running(elsewhere) || !strings.Contains(out.String(), "Nothing of heedle's was running") {
		t.Errorf("down stopped a process that isn't heedle's (decoy %v, other home's Temporal %v):\n%s", running(decoy), running(elsewhere), out.String())
	}
	// The clean start leaves them too, and a busy port still fails.
	write(st)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	m := &machine{goos: "linux", has: map[string]bool{"git": true, "temporal": true}}
	upSys := m.system()
	upSys.procArgs, upSys.signal = sys.procArgs, sys.signal
	err = up(ctx, []string{"-home", home, "-temporal-port", fmt.Sprint(busy.Addr().(*net.TCPAddr).Port), "-temporal-ui-port", fmt.Sprint(freePort(t))}, io.Discard, upSys)
	if err == nil || !strings.Contains(err.Error(), "in use") || m.started != nil || !running(decoy) || !running(elsewhere) {
		t.Errorf("up with a decoy on the port: %v; decoy %v, other Temporal %v", err, running(decoy), running(elsewhere))
	}

	// heedle's own Temporal, with this home's database, is stopped; so is
	// heedle, by an interrupt; and one that ignores it is killed.
	ours, oursArgs := standIn(t, true, temporalArgs(home)...)
	heedle, heedleArgs := standIn(t, false, "up", "-home", home)
	st = upState{PID: heedle, Args: heedleArgs, UI: "http://127.0.0.1:8090"}
	st.Temporal.PID, st.Temporal.Args = ours, oursArgs
	write(st)
	out.Reset()
	if err := down(ctx, []string{"-home", home}, &out, sys); err != nil {
		t.Fatal(err)
	}
	if running(heedle) || running(ours) || !running(decoy) || !running(elsewhere) {
		t.Errorf("down: heedle %v, its Temporal %v, decoy %v, other Temporal %v", running(heedle), running(ours), running(decoy), running(elsewhere))
	}
	if !strings.Contains(out.String(), fmt.Sprintf("heedle (pid %d, http://127.0.0.1:8090)", heedle)) || !strings.Contains(out.String(), fmt.Sprintf("Temporal (pid %d)", ours)) {
		t.Errorf("down said:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, upFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("up.json outlived heedle down")
	}
	// A process ID that has gone is left alone.
	write(st)
	out.Reset()
	if err := down(ctx, []string{"-home", home}, &out, sys); err != nil || !strings.Contains(out.String(), "Nothing of heedle's was running") {
		t.Errorf("down with nothing left: %v\n%s", err, out.String())
	}
}

// TestInv_UpStartsCleanly: heedle up refuses while this home's heedle
// runs, saying where and that heedle down stops it; with only the Temporal
// a killed heedle left, it stops that one, says so, and starts
// (HEEDLE-502).
func TestInv_UpStartsCleanly(t *testing.T) {
	sys := system{goos: goos()}.withDefaults(io.Discard)
	home := t.TempDir()
	heedle, heedleArgs := standIn(t, false, "up", "-home", home)
	st := upState{PID: heedle, Args: heedleArgs, Home: home, UI: "http://127.0.0.1:8123"}
	writeUpState(home, st)
	m := &machine{goos: "linux", has: map[string]bool{"git": true, "temporal": true}}
	upSys := m.system()
	upSys.procArgs, upSys.signal = sys.procArgs, sys.signal
	ports := []string{"-temporal-port", fmt.Sprint(freePort(t)), "-temporal-ui-port", fmt.Sprint(freePort(t))}
	err := up(ctx, append([]string{"-home", home}, ports...), io.Discard, upSys)
	if err == nil || err.Error() != "heedle is already running at http://127.0.0.1:8123; heedle down stops it" || m.started != nil || !running(heedle) {
		t.Fatalf("up with heedle running: %v (heedle still running: %v)", err, running(heedle))
	}

	// heedle was killed: only its Temporal is left.
	syscall.Kill(heedle, syscall.SIGKILL)
	for i := 0; i < 100 && running(heedle); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	ours, oursArgs := standIn(t, false, temporalArgs(home)...)
	st.Temporal.PID, st.Temporal.Args = ours, oursArgs
	writeUpState(home, st)
	var out bytes.Buffer
	if err := up(ctx, append([]string{"-home", home}, ports...), &out, upSys); err != nil {
		t.Fatal(err)
	}
	if running(ours) || m.started == nil || !strings.Contains(out.String(), fmt.Sprintf("Stopped the Temporal a previous heedle up left running (pid %d).", ours)) {
		t.Errorf("the clean start: leftover Temporal running %v, started %v\n%s", running(ours), m.started != nil, out.String())
	}
}

// podmanStandIn puts a fake podman on the PATH: ps lists one of heedle's
// containers and one of yours (as if the filter let it through), and
// every call is logged.
func podmanStandIn(t *testing.T) (string, string) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	heedleID, mineID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	ps := `[{"Id":"` + heedleID + `","Names":["bobbin-session-1"],"State":"running","Created":1,"Labels":{"heedle.kind":"agent"}},{"Id":"` + mineID + `","Names":["mine"],"State":"running","Created":1,"Labels":{}}]`
	sh := "#!/bin/sh\necho \"$@\" >> " + log + "\ncase \"$1\" in ps) echo '" + ps + "';; stats) echo '[]';; images) echo '" +
		`[{"Id":"1","Names":["` + pinned.Agent + `"],"Size":1,"Containers":0},{"Id":"2","Names":["ghcr.io/heedle-ai/pirn-agent@sha256:` + strings.Repeat("0", 64) + `"],"Size":1,"Containers":0},{"Id":"3","Names":["localhost/mine:1"],"Size":1,"Containers":0}]` +
		"';; esac\n"
	os.WriteFile(filepath.Join(dir, "podman"), []byte(sh), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return filepath.Join(dir, "podman"), log
}

// TestInv_DownRemovesOnlyHeedlesContainers: heedle down removes heedle's
// labelled containers, and only those (bobbin's listing), and says so
// (HEEDLE-503).
func TestInv_DownRemovesOnlyHeedlesContainers(t *testing.T) {
	podman, log := podmanStandIn(t)
	sys := system{goos: goos(), lookPath: func(name string) (string, error) {
		if name == "podman" {
			return podman, nil
		}
		return "", errors.New("not found")
	}}
	var out bytes.Buffer
	if err := down(ctx, []string{"-home", t.TempDir()}, &out, sys); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "rm --force --time 0 --ignore "+strings.Repeat("a", 64)+"\n") || strings.Contains(string(b), strings.Repeat("b", 64)) {
		t.Errorf("podman calls:\n%s", b)
	}
	if out.String() != "Stopped 1 of heedle's containers.\n" {
		t.Errorf("down said %q", out.String())
	}
}

// TestInv_ImagesKeepsTheReleasesPins: heedle images keeps this release's
// pinned images and those the running heedle up gave its worker: a prune
// removes only an old heedle image, never a pinned one or your own
// (HEEDLE-504).
func TestInv_ImagesKeepsTheReleasesPins(t *testing.T) {
	_, log := podmanStandIn(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	st := upState{Keep: []string{"localhost/given:1"}}
	os.MkdirAll(filepath.Join(home, ".heedle"), 0o700)
	writeUpState(filepath.Join(home, ".heedle"), st)
	want := []string{}
	for _, r := range append(pinned.refs(), "localhost/given:1") {
		want = append(want, "-keep", r)
	}
	if got := keepArgs(defaultHome()); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("keepArgs = %q", got)
	}
	var out bytes.Buffer
	if err := run(ctx, []string{"images", "-prune"}, &out); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	rm := ""
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "image rm") {
			rm += l + "\n"
		}
	}
	if rm != "image rm ghcr.io/heedle-ai/pirn-agent@sha256:"+strings.Repeat("0", 64)+"\n" {
		t.Errorf("heedle images -prune removed:\n%s", rm)
	}
}

// TestE2E_KillThenUpThenDown: heedle up, for real (the temporal CLI on
// the PATH), killed with kill -9, leaves its Temporal running; the next
// heedle up stops that one, says so, and starts; heedle down stops
// everything, and the ports are free again (HEEDLE-505). CI's e2e step
// runs it with HEEDLE_E2E=1.
func TestE2E_KillThenUpThenDown(t *testing.T) {
	if os.Getenv("HEEDLE_E2E") != "1" {
		t.Skip("set HEEDLE_E2E=1 to run heedle up against a real Temporal server")
	}
	bin := filepath.Join(t.TempDir(), "heedle")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	home := t.TempDir()
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	port, uiPort := freePort(t), freePort(t)
	args := []string{"up", "-home", home, "-listen", listen, "-notify=false", "-temporal-port", fmt.Sprint(port), "-temporal-ui-port", fmt.Sprint(uiPort)}
	start := func() (*exec.Cmd, *syncBuffer) {
		var out syncBuffer
		cmd := exec.Command(bin, args...)
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		for i := 0; i < 300; i++ {
			if c, err := net.Dial("tcp", listen); err == nil {
				c.Close()
				return cmd, &out
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("heedle up never served:\n%s", out.String())
		return nil, nil
	}
	first, _ := start()
	st, err := readUpState(home)
	if err != nil || st == nil || st.PID != first.Process.Pid || st.Temporal.PID == 0 {
		t.Fatalf("up.json: %+v %v", st, err)
	}
	first.Process.Signal(syscall.SIGKILL)
	first.Wait()
	if !running(st.Temporal.PID) {
		t.Fatal("killing heedle up stopped its Temporal too; this test needs it left running")
	}

	second, out := start()
	if !strings.Contains(out.String(), fmt.Sprintf("Stopped the Temporal a previous heedle up left running (pid %d).", st.Temporal.PID)) || running(st.Temporal.PID) {
		t.Errorf("the second heedle up didn't clean up:\n%s", out.String())
	}
	st2, _ := readUpState(home)
	var down bytes.Buffer
	cmd := exec.Command(bin, "down", "-home", home)
	cmd.Stdout, cmd.Stderr = &down, &down
	if err := cmd.Run(); err != nil {
		t.Fatalf("heedle down: %v\n%s", err, down.String())
	}
	second.Wait()
	if !strings.HasPrefix(down.String(), fmt.Sprintf("Stopped heedle (pid %d, http://%s) and its worker, with its Temporal", second.Process.Pid, listen)) {
		t.Errorf("heedle down said:\n%s", down.String())
	}
	if st2 == nil || running(st2.Temporal.PID) {
		t.Error("heedle down left Temporal running")
	}
	for _, a := range []string{listen, fmt.Sprintf("127.0.0.1:%d", port)} {
		if c, err := net.Dial("tcp", a); err == nil {
			c.Close()
			t.Errorf("%s still answers after heedle down", a)
		}
	}
	if _, err := os.Stat(filepath.Join(home, upFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("up.json outlived heedle down")
	}
}
