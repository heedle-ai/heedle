package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/testsuite"

	"github.com/heedle-ai/draft"
	"github.com/heedle-ai/loom"
	"github.com/heedle-ai/pirn"
	"github.com/heedle-ai/reed"
	"github.com/heedle-ai/warp"
)

// TestE2EWheel_PersonSignsIn is ci:e2e-course's run of taking the wheel
// (roadmap 9.10), in the browser-agent image: a stand-in Claude opens the
// course's sign-in form and calls ask_person; a person, through the step's
// Wheels, as the run page drives it, presses Tab and types the email, the
// password and an address, and Enter; the form arrives, sent by them; they
// hand back, and Claude carries on from the page they left. What they
// typed is nowhere afterwards: not in the run's Git store, not in what
// Temporal kept (results and heartbeats), not in the logs, not in any
// request to Claude.
func TestE2EWheel_PersonSignsIn(t *testing.T) {
	if os.Getenv("HEEDLE_E2E_ACT") != "1" {
		t.Skip("set HEEDLE_E2E_ACT=1 to run the course of forms with the browser-agent image")
	}
	img := browserAgentImage(t)
	c := startCourse(t)
	typed := map[string]string{"email": "ada.typed@example.org", "password": "hunter2-typed", "address": "12 Typed Street"}
	var toClaude []string
	claude := scriptedSeeing(t, &toClaude, open(t, "password"), snapshot(t),
		func([]string) string {
			return `{"type":"tool_use","id":"p1","name":"ask_person","input":{"reason":"The form asks you to sign in."}}`
		},
		snapshot(t), say("You signed in and saved the address.\nResult: finished"))
	wheels := &pirn.Wheels{}
	act := pirn.Act{Claude: pirn.Claude{APIKey: "stand-in", BaseURL: claude.URL}, Wheels: wheels,
		Browser: pirn.ReedBrowser{Browser: reed.Browser{Image: img, Network: c.network}}, Image: img}
	program := `(act "Change my delivery address on my account." "http://forms-srv:8080/password/")`
	var kept, logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	reg := draft.DefaultRegistry()
	var suite testsuite.WorkflowTestSuite
	suite.SetLogger(tlog.NewStructuredLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(pirn.ActBudget + 5*time.Minute)
	driven := make(chan bool, 1)
	env.SetOnActivityHeartbeatListener(func(_ *activity.Info, details converter.EncodedValues) {
		var b pirn.ActBeat
		details.Get(&b)
		j, _ := json.Marshal(b)
		kept.Write(j)
		if b.Wheel == nil {
			return
		}
		select {
		case driven <- true: // the first beat asking: a person drives
		default:
			return
		}
		go func(id string) {
			w, ok := wheels.Wheel(id)
			if !ok {
				t.Error("the step waiting for a person isn't in the registry")
				return
			}
			key := func(k string) {
				if err := w.Key(k); err != nil {
					t.Errorf("%q: %v", k, err)
				}
			}
			key("Tab")
			for _, f := range []string{"email", "password", "address"} {
				chars := strings.Split(typed[f], "")
				if f == "email" {
					// A typo, fixed through Playwright MCP's real keyboard:
					// the third character left out, then put back (review 52).
					for _, ch := range append(chars[:2:2], chars[3:]...) {
						key(ch)
					}
					for _, k := range []string{"Home", "ArrowRight", "ArrowRight", chars[2], "End"} {
						key(k)
					}
				} else {
					for _, ch := range chars {
						key(ch)
					}
				}
				key("Tab")
			}
			key("Enter")
			time.Sleep(time.Second)
			w.HandBack()
		}(b.Wheel.ID)
	})
	env.SetOnActivityCompletedListener(func(_ *activity.Info, result converter.EncodedValue, err error) {
		var out any
		if result != nil && result.HasValue() {
			result.Get(&out)
		}
		j, _ := json.Marshal(out)
		kept.Write(j)
		if err != nil {
			kept.WriteString(err.Error())
		}
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
	j, _ := json.Marshal(res)
	kept.Write(j)
	rec, ok := pirn.ParseAct(res.Outputs["n0"])
	if !ok || rec.Ended != pirn.EndedFinished {
		t.Fatalf("record: %s", res.Outputs["n0"])
	}
	// Sent by the person, and only by them.
	got := c.check("password").Received
	if len(got) != 1 || !strings.Contains(got[0], "address=12+Typed+Street") || strings.Join(rec.SentByYou, " ") != "POST http://forms-srv:8080/password/submit" {
		t.Fatalf("received %v; sent by you %v", got, rec.SentByYou)
	}
	// Claude carries on from the page the person left.
	var asked pirn.ActCall
	for _, call := range rec.Calls {
		if call.Tool == pirn.AskPersonTool {
			asked = call
		}
	}
	if asked.IsError || asked.URL != "http://forms-srv:8080/password/submit" || !strings.Contains(asked.Output, "a person acted") || !strings.Contains(asked.Output, "Thank you") || asked.Shot == "" {
		t.Errorf("hand back: %+v", asked)
	}
	objects, err := exec.Command("git", "-C", dir, "cat-file", "--batch-all-objects", "--batch").Output()
	if err != nil || len(objects) == 0 || kept.Len() == 0 || len(toClaude) < 3 {
		t.Fatalf("nothing kept: the search proves nothing (%v)", err)
	}
	for _, v := range typed {
		for where, text := range map[string]string{"the store": string(objects), "Temporal": kept.String(), "the logs": logs.String(), "Claude's requests": strings.Join(toClaude, "\n")} {
			for _, form := range []string{v, strings.ReplaceAll(v, " ", "+"), strings.ReplaceAll(v, "@", "%40")} {
				if strings.Contains(text, form) {
					t.Errorf("%q is in %s", form, where)
				}
			}
		}
	}
}
