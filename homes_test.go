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
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/heedle-ai/bobbin"
)

// ownHomes points the shared folder of running homes at a folder of the
// test's own.
func ownHomes(t *testing.T) {
	dir := t.TempDir()
	old := homesDir
	homesDir = func() string { return dir }
	t.Cleanup(func() { homesDir = old })
}

// runningHome makes home look like a running heedle up's: a stand-in
// heedle under up.json, and the home said in the shared folder. It
// returns up.json's state.
func runningHome(t *testing.T, home string, keep ...string) upState {
	t.Helper()
	pid, args := standIn(t, false, "up", "-home", home)
	st := upState{PID: pid, Args: args, Home: home, UI: "http://127.0.0.1:8090", HomeLabel: bobbin.HomeOf(filepath.Join(home, "store.git")), Keep: keep}
	if err := writeUpState(home, st); err != nil {
		t.Fatal(err)
	}
	if _, err := registerHome(home, st.HomeLabel); err != nil {
		t.Fatal(err)
	}
	return st
}

// homesPodman is a fake podman whose ps lists containers of homes a and
// b, one from before homes were labelled, and one whose home label isn't
// one; every call is logged.
func homesPodman(t *testing.T, a, b string) (string, string) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	ctr := func(id, home string) string {
		l := `"heedle.kind":"agent"`
		if home != "" {
			l += `,"heedle.home":"` + home + `"`
		}
		return `{"Id":"` + strings.Repeat(id, 64) + `","Names":["c` + id + `"],"State":"running","Created":1,"Labels":{` + l + `}}`
	}
	ps := "[" + strings.Join([]string{ctr("a", a), ctr("b", b), ctr("c", ""), ctr("d", "--x")}, ",") + "]"
	sh := "#!/bin/sh\necho \"$@\" >> " + log + "\ncase \"$1\" in ps) echo '" + ps + "';; stats) echo '[]';; esac\n"
	os.WriteFile(filepath.Join(dir, "podman"), []byte(sh), 0o755)
	return filepath.Join(dir, "podman"), log
}

// removed are the container IDs a fake podman's log says were removed,
// each by its first character.
func removed(t *testing.T, log string) string {
	b, _ := os.ReadFile(log)
	var out string
	for _, l := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(l, "rm --force --time 0 --ignore "); ok {
			for _, id := range strings.Fields(rest) {
				out += id[:1]
			}
		}
	}
	os.Remove(log)
	return out
}

// TestInv_DownTouchesOnlyThisHome: with heedle running from two homes,
// heedle down for one removes that home's containers and no other's; a
// container from before homes were labelled stays while another home's
// heedle runs, and down says so; once no other home runs, it goes; one
// whose home label isn't a home never does; a home whose heedle was
// killed doesn't count as running (HEEDLE-520, roadmap 9.2.1).
func TestInv_DownTouchesOnlyThisHome(t *testing.T) {
	ownHomes(t)
	homeA, homeB := t.TempDir(), t.TempDir()
	stA, stB := runningHome(t, homeA), runningHome(t, homeB)
	podman, log := homesPodman(t, stA.HomeLabel, stB.HomeLabel)
	sys := system{goos: goos(), lookPath: func(name string) (string, error) {
		if name == "podman" {
			return podman, nil
		}
		return "", errors.New("not found")
	}}.withDefaults(io.Discard)
	if got := runningHomes(ctx, sys); strings.Join(got, " ") != strings.Join(sortedPair(homeA, homeB), " ") {
		t.Fatalf("running homes: %q", got)
	}
	// A home whose heedle was killed, its file still there, isn't running.
	stale := t.TempDir()
	writeUpState(stale, upState{PID: 1 << 22, Args: []string{"/usr/local/bin/heedle", "up"}, Home: stale})
	registerHome(stale, bobbin.HomeOf(filepath.Join(stale, "store.git")))
	if got := runningHomes(ctx, sys); len(got) != 2 {
		t.Errorf("a killed heedle's home counts as running: %q", got)
	}

	var out bytes.Buffer
	if err := down(ctx, []string{"-home", homeA}, &out, sys); err != nil {
		t.Fatal(err)
	}
	if got := removed(t, log); got != "a" {
		t.Errorf("down for home A removed %q, want only A's", got)
	}
	if !strings.Contains(out.String(), "1 of this home's containers") || !strings.Contains(out.String(), "Left 1 container from before heedle labelled homes, since heedle runs from another home too ("+homeB+")") {
		t.Errorf("down for home A said:\n%s", out.String())
	}
	if !running(stB.PID) {
		t.Fatal("down for home A stopped home B's heedle")
	}
	// Now only B runs: the old container goes with B's, never the one
	// whose label isn't a home.
	out.Reset()
	if err := down(ctx, []string{"-home", homeB}, &out, sys); err != nil {
		t.Fatal(err)
	}
	if got := removed(t, log); got != "bc" || strings.Contains(out.String(), "Left") {
		t.Errorf("down for home B removed %q:\n%s", got, out.String())
	}
}

func sortedPair(a, b string) []string {
	if a > b {
		return []string{b, a}
	}
	return []string{a, b}
}

// TestInv_PruneKeepsEveryRunningHomesPins: heedle images -prune, for any
// home or none, keeps the images every running home's heedle up pinned
// (its release's) or gave its worker; a home whose heedle isn't running
// keeps nothing (HEEDLE-521, roadmap 9.2.1).
func TestInv_PruneKeepsEveryRunningHomesPins(t *testing.T) {
	ownHomes(t)
	digest := func(c string) string { return "@sha256:" + strings.Repeat(c, 64) }
	b := "ghcr.io/heedle-ai/pirn-agent" + digest("b")
	oldRelease := "ghcr.io/heedle-ai/reed-browser" + digest("e")
	stale := "ghcr.io/heedle-ai/pirn-agent" + digest("c")
	old := "ghcr.io/heedle-ai/pirn-agent" + digest("0")
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	images := `[{"Id":"1","Names":["` + pinned.Agent + `"],"Size":1,"Containers":0},{"Id":"2","Names":["` + b + `"],"Size":1,"Containers":0},{"Id":"3","Names":["` + stale + `"],"Size":1,"Containers":0},{"Id":"4","Names":["` + old + `"],"Size":1,"Containers":0},{"Id":"5","Names":["` + oldRelease + `"],"Size":1,"Containers":0}]`
	os.WriteFile(filepath.Join(dir, "podman"), []byte("#!/bin/sh\necho \"$@\" >> "+log+"\ncase \"$1\" in images) echo '"+images+"';; esac\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())

	homeB := t.TempDir()
	st := runningHome(t, homeB, b)
	st.Pins = []string{oldRelease} // B runs another release
	writeUpState(homeB, st)
	homeC := t.TempDir()
	writeUpState(homeC, upState{PID: 1 << 22, Args: []string{"/usr/local/bin/heedle", "up"}, Home: homeC, Keep: []string{stale}})
	registerHome(homeC, bobbin.HomeOf(filepath.Join(homeC, "store.git")))
	for _, args := range [][]string{{"images", "-prune"}, {"images", "-home", t.TempDir(), "-prune"}} {
		var out bytes.Buffer
		if err := run(ctx, args, &out); err != nil {
			t.Fatal(err)
		}
		bs, _ := os.ReadFile(log)
		os.Remove(log)
		var rm []string
		for _, l := range strings.Split(string(bs), "\n") {
			if n, ok := strings.CutPrefix(l, "image rm "); ok {
				rm = append(rm, n)
			}
		}
		if strings.Join(rm, " ") != stale+" "+old {
			t.Errorf("heedle %q removed %q, want only the old image and the one a home that isn't running gave", args, rm)
		}
	}
}

// wrapped starts a temporal wrapper that runs a stand-in Temporal with
// home's database as its child, without exec, listening on port: its
// process and the child's.
func wrapped(t *testing.T, home string, port int) (wrapper int, child int, childLine string) {
	t.Helper()
	dir := t.TempDir()
	w := filepath.Join(dir, "temporal")
	os.WriteFile(w, []byte("#!/bin/sh\n\"$@\"\n"), 0o755)
	self, _ := os.Executable()
	argv := append([]string{self, "-test.run=^$", "--"}, temporalArgs(home)...)
	cmd := exec.Command(w, argv...)
	cmd.Env = append(os.Environ(), "HEEDLE_STANDIN=1", fmt.Sprintf("HEEDLE_STANDIN_LISTEN=%d", port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	sys := system{goos: goos()}.withDefaults(io.Discard)
	for i := 0; i < 250; i++ {
		if pid, err := sys.listener(ctx, port); err == nil && pid != cmd.Process.Pid {
			t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL); cmd.Process.Kill() })
			line, _ := sys.procArgs(ctx, pid)
			return cmd.Process.Pid, pid, line
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the wrapped stand-in never listened")
	return 0, 0, ""
}

// TestInv_WrapperStartedTemporalIsStopped: when the temporal on the PATH
// is a wrapper that runs the real one as its child, without exec, heedle
// up records the process answering on the port, and stops it when it
// stops; heedle down stops it after heedle was killed; and a Temporal
// heedle can't recognise (an up.json that doesn't record it) is left
// running, with down saying so and failing, never claiming it stopped
// (HEEDLE-522, roadmap 9.2.1).
func TestInv_WrapperStartedTemporalIsStopped(t *testing.T) {
	if _, err := exec.LookPath("lsof"); err != nil {
		t.Fatal("this test needs lsof, as heedle does to see which process answers on Temporal's port")
	}
	ownHomes(t)
	real := system{goos: goos()}.withDefaults(io.Discard)

	// heedle up stops the wrapper's child too.
	home := t.TempDir()
	port := freePort(t)
	var child int
	m := &machine{goos: "linux", has: map[string]bool{"git": true, "temporal": true}}
	sys := m.system()
	sys.procArgs, sys.signal, sys.listener = real.procArgs, real.signal, real.listener
	var seen *upState
	sys.temporal = func(_ context.Context, _ string, _ []string, _ io.Writer, _ string) (func() error, int, error) {
		w, c, _ := wrapped(t, home, port)
		child = c
		return func() error { syscall.Kill(w, syscall.SIGINT); return nil }, w, nil
	}
	sys.serve = func(context.Context, []string, io.Writer) error {
		seen, _ = readUpState(home)
		return nil
	}
	var out bytes.Buffer
	if err := up(ctx, []string{"-home", home, "-notify=false", "-temporal-port", fmt.Sprint(port), "-temporal-ui-port", fmt.Sprint(freePort(t))}, &out, sys); err != nil {
		t.Fatal(err)
	}
	if seen == nil || seen.Temporal.Listener == nil || seen.Temporal.Listener.PID != child {
		t.Errorf("up.json didn't record the wrapper's child %d: %+v", child, seen)
	}
	if running(child) || answers(fmt.Sprintf("127.0.0.1:%d", port)) {
		t.Errorf("heedle up left the wrapper's child running:\n%s", out.String())
	}

	// heedle was killed: down stops the wrapper and its child.
	home = t.TempDir()
	port = freePort(t)
	w, c, line := wrapped(t, home, port)
	st := upState{PID: 1 << 22, Args: []string{"/usr/local/bin/heedle", "up"}, Home: home, UI: "http://127.0.0.1:8090", TemporalPort: port}
	wline, _ := real.procArgs(ctx, w)
	st.Temporal.PID, st.Temporal.Args, st.Temporal.Cmdline = w, append([]string{"/opt/wrappers/temporal"}, temporalArgs(home)...), wline
	st.Temporal.Listener = &process{PID: c, Cmdline: line}
	writeUpState(home, st)
	out.Reset()
	if err := down(ctx, []string{"-home", home}, &out, real); err != nil {
		t.Fatal(err)
	}
	if running(c) || running(w) || !strings.Contains(out.String(), fmt.Sprintf("Temporal (pid %d, started by its wrapper)", c)) {
		t.Errorf("down after a kill: child %v, wrapper %v:\n%s", running(c), running(w), out.String())
	}

	// An up.json from before 9.2.1 doesn't record the child: down stops
	// heedle and the wrapper, then says Temporal still answers and fails.
	home = t.TempDir()
	port = freePort(t)
	w, c, _ = wrapped(t, home, port)
	heedle, heedleArgs := standIn(t, false, "up", "-home", home)
	st = upState{PID: heedle, Args: heedleArgs, Home: home, UI: "http://127.0.0.1:8090", TemporalPort: port}
	wline, _ = real.procArgs(ctx, w)
	st.Temporal.PID, st.Temporal.Args, st.Temporal.Cmdline = w, append([]string{"/opt/wrappers/temporal"}, temporalArgs(home)...), wline
	writeUpState(home, st)
	out.Reset()
	err := down(ctx, []string{"-home", home}, &out, real)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("something still answers on Temporal's port %d (pid %d)", port, c)) || !running(c) {
		t.Errorf("down with a Temporal it can't recognise: %v (child running %v)\n%s", err, running(c), out.String())
	}
	if strings.Contains(out.String(), "with its Temporal") || strings.Contains(out.String(), "Temporal (pid") || !strings.Contains(out.String(), fmt.Sprintf("the process heedle started Temporal with (pid %d)", w)) {
		t.Errorf("down claimed Temporal stopped while it still answers:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, upFile)); err != nil {
		t.Error("up.json went while Temporal still answers")
	}
}

// TestInv_ContainersAreThisHomes: heedle containers gives loom's this
// home's store and the Temporal its heedle up started, unless given: so
// the listing marks this home's containers and -clean removes only those
// (HEEDLE-523, roadmap 9.2.1).
func TestInv_ContainersAreThisHomes(t *testing.T) {
	home := t.TempDir()
	writeUpState(home, upState{TemporalPort: 7299})
	store := filepath.Join(home, "store.git")
	if got := homeArgs(home, nil); strings.Join(got, " ") != "-store "+store+" -address 127.0.0.1:7299" {
		t.Errorf("homeArgs = %q", got)
	}
	if got := homeArgs(home, []string{"-store", "x", "-address=y"}); len(got) != 0 {
		t.Errorf("homeArgs with both given = %q", got)
	}
	if got := homeArgs(t.TempDir(), nil); len(got) != 2 || got[0] != "-store" {
		t.Errorf("homeArgs for a home not running = %q", got)
	}
	podman, _ := homesPodman(t, bobbin.HomeOf(store), "bbbbbbbbbbbb")
	t.Setenv("PATH", filepath.Dir(podman)+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeUpState(home, upState{TemporalPort: freePort(t)})
	var out bytes.Buffer
	if err := run(ctx, []string{"containers", "-home", home}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "This home is "+bobbin.HomeOf(store)) || strings.Count(out.String(), "(this)") != 1 || !strings.Contains(out.String(), "bbbbbbbbbbbb") {
		t.Errorf("heedle containers -home:\n%s", out.String())
	}
	// A relative -home is the same home.
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(filepath.Dir(home))
	if h, _, _ := homeArg([]string{"-home", filepath.Base(home)}); h != home {
		t.Errorf("a relative -home is %q, not %q", h, home)
	}
}

// answersAt reports whether something listens on 127.0.0.1:port.
func answersAt(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
