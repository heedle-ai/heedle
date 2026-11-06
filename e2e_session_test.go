package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"

	"github.com/heedle-ai/draft"
	"github.com/heedle-ai/loom"
	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/reed"
	"github.com/heedle-ai/warp"
)

// keys is a keychain in memory.
type keys map[string]string

func (k keys) Lookup(_ context.Context, n string) (string, bool, error) {
	v, ok := k[n]
	return v, ok, nil
}
func (k keys) Store(_ context.Context, n, v string) error { k[n] = v; return nil }
func (k keys) Delete(_ context.Context, n string) error   { delete(k, n); return nil }

// TestE2ESession_KeptSignIn is ci:e2e-course's run of saved sign-ins
// (roadmap 9.11), in the browser-agent image: a person takes the wheel,
// signs in to the course's account page (whose site then knows them by a
// cookie) and hands back keeping the sign-in; a later run of another
// program on that site starts signed in as them, its first page saying
// so, and its record says so. The kept file holds none of the cookie, and
// the cookie is in no store, no Temporal history, no log and no request to
// Claude; forgotten, the next run starts signed out.
func TestE2ESession_KeptSignIn(t *testing.T) {
	if os.Getenv("HEEDLE_E2E_ACT") != "1" {
		t.Skip("set HEEDLE_E2E_ACT=1 to run the course of forms with the browser-agent image")
	}
	img := browserAgentImage(t)
	c := startCourse(t)
	vault := keys{}
	sessions := &pirn.Sessions{Dir: filepath.Join(t.TempDir(), "sessions"), Vault: vault}
	var kept, logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	run := func(program string, toClaude *[]string, drive func(w *pirn.Wheel), moves ...move) pirn.ActResult {
		t.Helper()
		claude := scriptedSeeing(t, toClaude, moves...)
		wheels := &pirn.Wheels{}
		act := pirn.Act{Claude: pirn.Claude{APIKey: "stand-in", BaseURL: claude.URL}, Wheels: wheels, Sessions: sessions,
			Browser: pirn.ReedBrowser{Browser: reed.Browser{Image: img, Network: c.network}}, Image: img}
		reg := draft.DefaultRegistry()
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.SetTestTimeout(pirn.ActBudget + 5*time.Minute)
		driven := make(chan bool, 1)
		env.SetOnActivityHeartbeatListener(func(_ *activity.Info, details converter.EncodedValues) {
			var b pirn.ActBeat
			details.Get(&b)
			j, _ := json.Marshal(b)
			kept.Write(j)
			if b.Wheel == nil || drive == nil {
				return
			}
			select {
			case driven <- true:
			default:
				return
			}
			go func(id string) {
				if w, ok := wheels.Wheel(id); ok {
					drive(w)
				} else {
					t.Error("the step waiting for a person isn't in the registry")
				}
			}(b.Wheel.ID)
		})
		env.SetOnActivityCompletedListener(func(_ *activity.Info, result converter.EncodedValue, err error) {
			var out any
			if result != nil && result.HasValue() {
				result.Get(&out)
			}
			j, _ := json.Marshal(out)
			kept.Write(j)
		})
		if err := pirn.RegisterAct(reg, env, act); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), "store.git")
		store, err := warp.Init(ctx, dir)
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
			t.Fatal(err)
		}
		var res loom.RunResult
		env.GetWorkflowResult(&res)
		objects, _ := exec.Command("git", "-C", dir, "cat-file", "--batch-all-objects", "--batch").Output()
		kept.Write(objects)
		rec, ok := pirn.ParseAct(res.Outputs["n0"])
		if !ok || rec.Ended != pirn.EndedFinished {
			t.Fatalf("record: %s", res.Outputs["n0"])
		}
		return rec
	}
	// Signed in by the person, kept.
	var first []string
	rec := run(`(act "Change my delivery address on my account." "http://forms-srv:8080/password/")`, &first, func(w *pirn.Wheel) {
		for _, k := range append(append(append([]string{"Tab"}, strings.Split("ada@example.org", "")...), "Tab"), append(strings.Split("hunter2-kept", ""), "Enter")...) {
			if err := w.Key(k); err != nil {
				t.Errorf("%q: %v", k, err)
			}
		}
		time.Sleep(time.Second)
		w.HandBack(true)
	}, open(t, "password"), func([]string) string {
		return `{"type":"tool_use","id":"p1","name":"ask_person","input":{"reason":"The form asks you to sign in."}}`
	}, say("You signed in.\nResult: finished"))
	if rec.Kept != "forms-srv" || len(rec.SignedIn) != 0 {
		t.Errorf("first run: kept %q, signed in %v", rec.Kept, rec.SignedIn)
	}
	file, err := os.ReadFile(filepath.Join(sessions.Dir, "forms-srv.session"))
	if err != nil || strings.Contains(string(file), "member-") || strings.Contains(string(file), "course-password") || len(vault) != 1 {
		t.Fatalf("the kept file: %v %s; keys %d", err, file, len(vault))
	}
	// A later run, another program on that site: signed in.
	var second []string
	rec = run(`(act "Check that I'm signed in." "http://forms-srv:8080/whoami/password")`, &second, nil,
		tool(t, "browser_navigate", map[string]any{"url": "http://forms-srv:8080/whoami/password"}), snapshot(t), say("You are signed in.\nResult: finished"))
	if strings.Join(rec.SignedIn, " ") != "forms-srv" || !strings.Contains(strings.Join(second, "\n"), "Signed in as a course member") {
		t.Errorf("second run: signed in %v; Claude saw %s", rec.SignedIn, strings.Join(second, "\n"))
	}
	// Forgotten: the next run starts signed out.
	if err := sessions.Forget(ctx, "forms-srv"); err != nil {
		t.Fatal(err)
	}
	var third []string
	rec = run(`(act "Check that I'm signed in." "http://forms-srv:8080/whoami/password")`, &third, nil,
		tool(t, "browser_navigate", map[string]any{"url": "http://forms-srv:8080/whoami/password"}), snapshot(t), say("You are signed out.\nResult: finished"))
	if len(rec.SignedIn) != 0 || !strings.Contains(strings.Join(third, "\n"), "Not signed in") || len(vault) != 0 {
		t.Errorf("after Forget: signed in %v; keys %d; Claude saw %s", rec.SignedIn, len(vault), strings.Join(third, "\n"))
	}
	// The cookie is nowhere it would show.
	all := map[string]string{"Temporal and the stores": kept.String(), "the logs": logs.String(), "Claude's requests": strings.Join(append(append(first, second...), third...), "\n")}
	for where, text := range all {
		if strings.Contains(text, "member-"+courseToken) {
			t.Errorf("the sign-in's cookie is in %s", where)
		}
	}
}
