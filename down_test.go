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
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/heedle-ai/bobbin"
)

// TestMain lets the test binary stand in for a process heedle may stop:
// with HEEDLE_STANDIN=1 it waits for an interrupt (as heedle and
// Temporal do), or, with HEEDLE_STANDIN_STUBBORN=1, ignores it.
func TestMain(m *testing.M) {
	if os.Getenv("HEEDLE_STANDIN") == "1" {
		// With HEEDLE_STANDIN_LISTEN=PORT it also listens on that port,
		// as Temporal does (roadmap 9.2.1).
		if port := os.Getenv("HEEDLE_STANDIN_LISTEN"); port != "" {
			l, err := net.Listen("tcp", "127.0.0.1:"+port)
			if err != nil {
				os.Exit(3)
			}
			go func() {
				for {
					c, err := l.Accept()
					if err != nil {
						return
					}
					c.Close()
				}
			}()
		}
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
	// No test says which homes run in the person's own ~/.heedle.
	dir, err := os.MkdirTemp("", "heedle-homes-")
	if err != nil {
		panic(err)
	}
	homesDir = func() string { return dir }
	// No test reaches the machine's own Podman, whose containers may be
	// anyone's: a podman that refuses goes first on the PATH, always. A
	// Podman end-to-end test puts the machine's back on its own PATH
	// (machinePodmanOnPath), for itself only (HEEDLE-530, review 55: a
	// test's heedle down removed other sessions' containers).
	if p, err := exec.LookPath("podman"); err == nil {
		machinePodman = p
	}
	guard := filepath.Join(dir, "guard")
	os.MkdirAll(guard, 0o700)
	os.WriteFile(filepath.Join(guard, "podman"), []byte("#!/bin/sh\necho \"heedle tests never use the machine's podman\" >&2\nexit 125\n"), 0o755)
	os.Setenv("PATH", guard+string(os.PathListSeparator)+os.Getenv("PATH"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// machinePodman is the machine's podman, found before the guard went on
// the PATH; "" without one.
var machinePodman string

// machinePodmanOnPath puts the machine's podman first on the PATH for
// this test only: for the Podman end-to-end tests, and nothing else.
func machinePodmanOnPath(t *testing.T) {
	t.Helper()
	if machinePodman == "" {
		t.Fatal("podman isn't on the PATH")
	}
	// A folder holding only podman, so nothing else of its folder (an
	// older go, say) comes first too (review 55).
	dir := t.TempDir()
	if err := os.Symlink(machinePodman, filepath.Join(dir, "podman")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
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
// to be reaped (a killed stand-in is a zombie until the test reaps it). A
// zombie, which reports no command line, has stopped: an orphan's may
// never be reaped where the first process doesn't reap them.
func running(pid int) bool {
	sys := system{goos: goos()}.withDefaults(io.Discard)
	for i := 0; i < 50; i++ {
		p, err := os.FindProcess(pid)
		if err != nil || p.Signal(syscall.Signal(0)) != nil {
			return false
		}
		if line, err := sys.procArgs(ctx, pid); err == nil && line == "" {
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
	sys.procArgs = func(_ context.Context, pid int) (string, error) {
		if pid != 4242 {
			return "", errors.New("no such process")
		}
		return "/opt/temporal/real-temporal server start-dev --db-filename " + filepath.Join(home, "temporal.db"), nil
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
	// Review 55: the label is the one the worker's store has, and the UI
	// is told where running homes say so.
	served := m.served[slices.Index(m.served, "-store")+1]
	if seen.HomeLabel != bobbin.HomeOf(served) || served != filepath.Join(home, "store.git") || !slices.Contains(m.served, "-homes") {
		t.Errorf("up.json's home label %q, the store served %q (label %q), the UI's flags %q", seen.HomeLabel, served, bobbin.HomeOf(served), m.served)
	}
	if seen.Temporal.Cmdline != "/opt/temporal/real-temporal server start-dev --db-filename "+filepath.Join(home, "temporal.db") {
		t.Errorf("up.json's Temporal command line is %q, not what the system reported", seen.Temporal.Cmdline)
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
	// Review 50: a temporal on the PATH that is a wrapper runs the real
	// one under another command line, which heedle up now records, and
	// which is what's matched (HEEDLE-509).
	wrapped, wrappedLine := standIn(t, false, temporalArgs(home)...)
	st = upState{PID: 1 << 22, Args: []string{"/usr/local/bin/heedle", "up"}, UI: "http://127.0.0.1:8090"}
	st.Temporal.PID, st.Temporal.Args, st.Temporal.Cmdline = wrapped, append([]string{"/opt/wrappers/temporal"}, temporalArgs(home)...), strings.Join(wrappedLine, " ")
	write(st)
	out.Reset()
	if err := down(ctx, []string{"-home", home}, &out, sys); err != nil {
		t.Fatal(err)
	}
	if running(wrapped) || !strings.Contains(out.String(), fmt.Sprintf("Temporal (pid %d)", wrapped)) {
		t.Errorf("down left the Temporal a wrapper started (running %v):\n%s", running(wrapped), out.String())
	}
	// A recorded command line without this home's database, or that isn't
	// a dev server, is never taken for heedle's Temporal.
	for _, line := range [][]string{temporalArgs(other), {"server", "start", "--db-filename", filepath.Join(home, "temporal.db")}} {
		p, pLine := standIn(t, false, line...)
		st.Temporal.PID, st.Temporal.Cmdline = p, strings.Join(pLine, " ")
		write(st)
		if err := down(ctx, []string{"-home", home}, io.Discard, sys); err != nil || !running(p) {
			t.Errorf("down stopped %q: %v", pLine, err)
		}
	}
	st = upState{PID: heedle, Args: heedleArgs, UI: "http://127.0.0.1:8090"}
	st.Temporal.PID, st.Temporal.Args = ours, oursArgs

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
	}}.withDefaults(io.Discard)
	// Review 50: heedle down for a home with nothing running removed the
	// containers of a heedle running from another home (HEEDLE-508).
	var out bytes.Buffer
	if err := down(ctx, []string{"-home", t.TempDir()}, &out, sys); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 || out.String() != "Nothing of heedle's was running.\n" {
		t.Errorf("down with nothing of this home's running: %q; podman calls:\n%s", out.String(), b)
	}
	home := t.TempDir()
	stale := upState{PID: 1 << 22, Args: []string{"/usr/local/bin/heedle", "up"}, Home: home, UI: "http://127.0.0.1:8090"}
	writeUpState(home, stale)
	if err := down(ctx, []string{"-home", home}, &out, sys); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Errorf("down with a stale up.json ran podman:\n%s", b)
	}
	// This home's heedle is running: down stops it, then removes heedle's
	// containers, and only heedle's.
	heedle, heedleArgs := standIn(t, false, "up", "-home", home)
	writeUpState(home, upState{PID: heedle, Args: heedleArgs, Home: home, UI: "http://127.0.0.1:8090"})
	out.Reset()
	if err := down(ctx, []string{"-home", home}, &out, sys); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "rm --force") || strings.Contains(string(b), strings.Repeat("b", 64)) {
		t.Errorf("podman calls:\n%s", b)
	}
	// Review 55: the old container (no home label) stays, since no
	// Temporal of this home vouched for its run.
	if want := fmt.Sprintf("Stopped heedle (pid %d, http://127.0.0.1:8090) and its worker, with its Temporal.\nLeft 1 container from before heedle labelled homes whose run this home's Temporal doesn't know: they may be another home's (heedle containers lists them).\n", heedle); out.String() != want {
		t.Errorf("down said %q, want %q", out.String(), want)
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
	if got := keepArgs(ctx, system{goos: goos()}.withDefaults(io.Discard), defaultHome()); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("keepArgs = %q", got)
	}
	// Review 50: -agent-image=X wasn't read as a pin, and heedle images
	// knew only the default home (HEEDLE-507).
	if got := keptImages([]string{"-browser-image", "a:1"}, []string{"-agent-image=b:1", "--browser-agent-image=c:1", "-store", "x", "-agent-image"}); strings.Join(got, " ") != "a:1 b:1 c:1" {
		t.Errorf("keptImages = %q", got)
	}
	other := t.TempDir()
	writeUpState(other, upState{Keep: []string{"localhost/other:1"}})
	for _, args := range [][]string{{"-home", other, "-prune"}, {"-home=" + other, "-prune"}, {"--home", other, "-prune"}} {
		h, rest, err := homeArg(args)
		if err != nil || h != other || strings.Join(rest, " ") != "-prune" {
			t.Errorf("homeArg(%q) = %q, %q, %v", args, h, rest, err)
		}
	}
	if h, rest, _ := homeArg([]string{"-prune"}); h != defaultHome() || strings.Join(rest, " ") != "-prune" {
		t.Errorf("homeArg with no -home = %q, %q", h, rest)
	}
	if _, _, err := homeArg([]string{"-home"}); err == nil {
		t.Error("-home with no directory was taken")
	}
	var out bytes.Buffer
	if err := run(ctx, []string{"images", "-home", other}, &out); err != nil || !strings.Contains(out.String(), "pirn-agent") {
		t.Errorf("heedle images -home: %v\n%s", err, out.String())
	}
	os.Remove(log)
	out.Reset()
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
	// heedle says which homes run in ~/.heedle/homes: a HOME of the
	// test's own, never the person's.
	env := append(os.Environ(), "HOME="+t.TempDir(), "PATH="+noContainers(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
	start := func() (*exec.Cmd, *syncBuffer) {
		var out syncBuffer
		cmd := exec.Command(bin, args...)
		cmd.Env = env
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
	cmd.Env = env
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
