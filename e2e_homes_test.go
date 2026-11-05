package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// heedleBin builds heedle once for a test.
func heedleBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "heedle")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// heedleUp starts bin up with args and env, and waits until its UI
// serves on listen.
func heedleUp(t *testing.T, bin string, env []string, listen string, args ...string) (*exec.Cmd, *syncBuffer) {
	t.Helper()
	var out syncBuffer
	cmd := exec.Command(bin, append([]string{"up", "-listen", listen, "-notify=false"}, args...)...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; i < 600; i++ {
		if answers(listen) {
			return cmd, &out
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("heedle up never served:\n%s", out.String())
	return nil, nil
}

// heedleRun runs bin with args and env, returning what it said.
func heedleRun(t *testing.T, bin string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestE2E_WrapperTemporalStops: for real, with the temporal on the PATH
// a wrapper script that runs the real CLI as its child (no exec): heedle
// up records the Temporal answering on its port, and heedle down stops
// it, says so, and frees the port (HEEDLE-525, roadmap 9.2.1). CI's e2e
// step runs it with HEEDLE_E2E=1.
func TestE2E_WrapperTemporalStops(t *testing.T) {
	if os.Getenv("HEEDLE_E2E") != "1" {
		t.Skip("set HEEDLE_E2E=1 to run heedle up against a real Temporal server")
	}
	temporal, err := exec.LookPath("temporal")
	if err != nil {
		t.Fatal("the temporal CLI isn't on the PATH")
	}
	bin := heedleBin(t)
	wrappers := t.TempDir()
	os.WriteFile(filepath.Join(wrappers, "temporal"), []byte("#!/bin/sh\n'"+temporal+"' \"$@\"\n"), 0o755)
	env := append(os.Environ(), "PATH="+wrappers+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+t.TempDir())
	home := t.TempDir()
	listen := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	port := freePort(t)
	first, _ := heedleUp(t, bin, env, listen, "-home", home, "-temporal-port", fmt.Sprint(port), "-temporal-ui-port", fmt.Sprint(freePort(t)))
	st, err := readUpState(home)
	if err != nil || st == nil || st.Temporal.Listener == nil || st.Temporal.Listener.PID == st.Temporal.PID {
		t.Fatalf("up.json doesn't record the Temporal the wrapper started: %+v %v", st, err)
	}
	child := st.Temporal.Listener.PID
	out, err := heedleRun(t, bin, env, "down", "-home", home)
	first.Wait()
	if err != nil || strings.Contains(out, "still answers") {
		t.Fatalf("heedle down: %v\n%s", err, out)
	}
	if running(child) || answers(fmt.Sprintf("127.0.0.1:%d", port)) {
		t.Errorf("heedle down left the Temporal the wrapper started running (pid %d):\n%s", child, out)
	}
}

// TestE2EHomes_TwoAtOnce: for real, heedle up from two homes at once,
// each with its own Temporal, worker and a container step running, in a
// Podman store of the test's own (so nothing else's containers are ever
// seen); and a container from before homes were labelled. heedle
// containers shows each home and marks this one's; heedle images -prune,
// for neither home, keeps what B's heedle up gave its worker; heedle down
// for A removes A's container and stops A's Temporal, leaving B's
// container, Temporal and UI running, and the old container, saying so;
// then heedle down for B removes B's and the old one (HEEDLE-524,
// roadmap 9.2.1). CI's e2e-homes step runs it with HEEDLE_E2E_HOMES=1 on
// Linux, where Podman runs.
func TestE2EHomes_TwoAtOnce(t *testing.T) {
	if os.Getenv("HEEDLE_E2E_HOMES") != "1" {
		t.Skip("set HEEDLE_E2E_HOMES=1 to run two homes at once against real Temporal servers and Podman")
	}
	for _, p := range []string{"podman", "temporal"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Fatalf("%s isn't on the PATH", p)
		}
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	bin := heedleBin(t)

	// A Podman store of the test's own: a podman on the PATH that is the
	// real one with its own storage (podman's own flags; its run folder
	// must be short).
	real, _ := exec.LookPath("podman")
	store, err := os.MkdirTemp("/tmp", "hh")
	if err != nil {
		t.Fatal(err)
	}
	wrappers := t.TempDir()
	os.WriteFile(filepath.Join(wrappers, "podman"), []byte(fmt.Sprintf("#!/bin/sh\nexec '%s' --root '%s/root' --runroot '%s/run' --tmpdir '%s/tmp' \"$@\"\n", real, store, store, store)), 0o755)
	pod := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(filepath.Join(wrappers, "podman"), args...).CombinedOutput()
		if err != nil {
			t.Fatalf("podman %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	t.Cleanup(func() {
		exec.Command(filepath.Join(wrappers, "podman"), "rm", "--all", "--force", "--time", "0").Run()
		exec.Command(filepath.Join(wrappers, "podman"), "rmi", "--all", "--force").Run()
		exec.Command(filepath.Join(wrappers, "podman"), "unshare", "rm", "-rf", store).Run()
		os.RemoveAll(store)
	})

	// Three small images: one whose entrypoint sleeps, for container
	// steps; one B's worker is given; and an old one nobody keeps.
	sleeper := filepath.Join(t.TempDir(), "sleep")
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "go.mod"), []byte("module sleeper\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nimport (\n\t\"os\"\n\t\"strconv\"\n\t\"time\"\n)\n\nfunc main() {\n\tn, _ := strconv.Atoi(os.Args[len(os.Args)-1])\n\ttime.Sleep(time.Duration(n) * time.Second)\n}\n"), 0o644)
	build := exec.Command("go", "build", "-o", sleeper, ".")
	build.Dir, build.Env = src, append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the sleeper: %v\n%s", err, out)
	}
	bin0, _ := os.ReadFile(sleeper)
	image := func(name, mark string) string {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		for _, f := range []struct {
			name string
			body []byte
			mode int64
		}{{"sleep", bin0, 0o755}, {"mark", []byte(mark), 0o644}} {
			tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), ModTime: time.Unix(0, 0)})
			tw.Write(f.body)
		}
		tw.Close()
		p := filepath.Join(t.TempDir(), "image.tar")
		os.WriteFile(p, b.Bytes(), 0o644)
		pod("import", "--change", `ENTRYPOINT ["/sleep"]`, p, name)
		return strings.TrimPrefix(pod("image", "inspect", "--format", "{{.Id}}", name), "sha256:")
	}
	sleep := image("ghcr.io/heedle-ai/e2e-sleeper:1", "sleeper")
	given := image("ghcr.io/heedle-ai/e2e-given-b:1", "b")
	image("ghcr.io/heedle-ai/e2e-old:1", "old")

	env := append(os.Environ(), "PATH="+wrappers+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+t.TempDir())
	type home struct {
		dir, listen string
		port        int
		label       string
		cmd         *exec.Cmd
		out         *syncBuffer
	}
	start := func(browser string) *home {
		h := &home{dir: t.TempDir(), listen: fmt.Sprintf("127.0.0.1:%d", freePort(t)), port: freePort(t)}
		// Each image given after --, so nothing is downloaded.
		h.cmd, h.out = heedleUp(t, bin, env, h.listen, "-home", h.dir, "-temporal-port", fmt.Sprint(h.port), "-temporal-ui-port", fmt.Sprint(freePort(t)),
			"--", "-browser-image", browser, "-agent-image", sleep, "-browser-agent-image", sleep)
		st, err := readUpState(h.dir)
		if err != nil || st == nil || st.HomeLabel == "" {
			t.Fatalf("up.json: %+v %v", st, err)
		}
		h.label = st.HomeLabel
		// A container step that runs ten minutes, on a loom worker of this
		// home's store (the UI's worker has no container command), on a
		// queue of its own.
		addr := fmt.Sprintf("127.0.0.1:%d", h.port)
		w := exec.Command(bin, "worker", "-store", filepath.Join(h.dir, "store.git"), "-address", addr, "-queue", "e2e-homes")
		w.Env, w.Stdout, w.Stderr = env, h.out, h.out
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { w.Process.Kill(); w.Wait() })
		r := exec.Command(bin, "run", "-address", addr, "-queue", "e2e-homes", fmt.Sprintf(`(container "%s" "600")`, sleep))
		r.Env, r.Stdout, r.Stderr = env, h.out, h.out
		if err := r.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Process.Kill(); r.Wait() })
		return h
	}
	a, b := start(sleep), start(given)
	if a.label == b.label {
		t.Fatal("two homes have the same label")
	}
	// containers lists the running containers of the test's store, by home.
	containers := func() map[string]int {
		var ps []struct {
			Labels map[string]string
			State  string
		}
		json.Unmarshal([]byte(pod("ps", "--all", "--filter", "label=heedle.kind", "--format", "json")), &ps)
		n := map[string]int{}
		for _, p := range ps {
			if p.State == "running" {
				n[p.Labels["heedle.home"]]++
			}
		}
		return n
	}
	for n := containers(); n[a.label] != 1 || n[b.label] != 1; n = containers() {
		select {
		case <-c.Done():
			t.Fatalf("the container steps never ran: %v\nA:\n%s\nB:\n%s", n, a.out.String(), b.out.String())
		case <-time.After(500 * time.Millisecond):
		}
	}
	// A container from before homes were labelled.
	pod("run", "-d", "--network=none", "--label", "heedle.kind=container", sleep, "600")

	out, err := heedleRun(t, bin, env, "containers", "-home", a.dir)
	if err != nil || !strings.Contains(out, a.label+" (this)") || !strings.Contains(out, b.label) || strings.Contains(out, b.label+" (this)") {
		t.Errorf("heedle containers -home A: %v\n%s", err, out)
	}
	// A prune for neither home keeps what B's heedle up gave its worker.
	out, err = heedleRun(t, bin, env, "images", "-prune")
	if err != nil || !strings.Contains(out, "Removed ghcr.io/heedle-ai/e2e-old:1") || strings.Contains(out, "e2e-given-b") {
		t.Errorf("heedle images -prune: %v\n%s", err, out)
	}
	if _, err := exec.Command(filepath.Join(wrappers, "podman"), "image", "exists", given).CombinedOutput(); err != nil {
		t.Error("the prune removed the image B's heedle up gave its worker")
	}

	out, err = heedleRun(t, bin, env, "down", "-home", a.dir)
	a.cmd.Wait()
	// A's own container may already have gone with its worker's step.
	if err != nil || !strings.Contains(out, "Left 1 container from before heedle labelled homes, since heedle runs from another home too ("+b.dir+")") {
		t.Errorf("heedle down -home A: %v\n%s", err, out)
	}
	if n := containers(); n[a.label] != 0 || n[b.label] != 1 || n[""] != 1 {
		t.Errorf("after down for A, running containers by home: %v", n)
	}
	if answers(fmt.Sprintf("127.0.0.1:%d", a.port)) || answers(a.listen) {
		t.Error("A's Temporal or UI still answers after its heedle down")
	}
	if !answers(fmt.Sprintf("127.0.0.1:%d", b.port)) || !answers(b.listen) {
		t.Fatal("heedle down for A stopped B's Temporal or UI")
	}

	out, err = heedleRun(t, bin, env, "down", "-home", b.dir)
	b.cmd.Wait()
	if err != nil || !strings.Contains(out, "of this home's containers") {
		t.Errorf("heedle down -home B: %v\n%s", err, out)
	}
	if n := containers(); len(n) != 0 {
		t.Errorf("after down for B, running containers by home: %v", n)
	}
	for _, addr := range []string{b.listen, fmt.Sprintf("127.0.0.1:%d", b.port)} {
		if conn, err := net.Dial("tcp", addr); err == nil {
			conn.Close()
			t.Errorf("%s still answers after B's heedle down", addr)
		}
	}
}
