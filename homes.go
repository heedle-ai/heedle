package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/heedle-ai/bobbin"
)

// Two homes at once (roadmap 9.2.1). heedle up can run from several homes
// at the same time, each with its own store, Temporal and ports. Each
// running heedle up says where its home is in a shared folder, so heedle
// down and heedle images can see which other homes are running: a file
// named for the home's label, holding the home's path. A home counts as
// running only while its up.json names a heedle or Temporal that is still
// exactly the one it started, so a file left by a heedle that was killed
// means nothing.

// homesDir is the shared folder: ~/.heedle/homes, whichever home heedle
// up runs from. Tests point it elsewhere.
var homesDir = func() string {
	if h := defaultHome(); h != "" {
		return filepath.Join(h, "homes")
	}
	return ""
}

// absHome is home as an absolute path, so a home is named the same
// however it was written.
func absHome(home string) string {
	if home == "" {
		return ""
	}
	if a, err := filepath.Abs(home); err == nil {
		return a
	}
	return home
}

// registerHome says that home's heedle up runs, under its label; the
// returned function takes that back.
func registerHome(home, label string) (func(), error) {
	dir := homesDir()
	if dir == "" || !bobbin.ValidHome(label) {
		return func() {}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, label)
	if err := os.WriteFile(p, []byte(home+"\n"), 0o600); err != nil {
		return nil, err
	}
	return func() { os.Remove(p) }, nil
}

// runningHomes are the homes whose heedle up is running now: those the
// shared folder names whose up.json still names a heedle or Temporal that
// is exactly the one it started. Sorted.
func runningHomes(ctx context.Context, sys system) []string {
	dir := homesDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var homes []string
	for _, e := range entries {
		if !bobbin.ValidHome(e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		home := strings.TrimSpace(string(b))
		st, err := readHomeState(home)
		if err == nil && st != nil && st.homeLabel(home) == e.Name() &&
			(isProcess(ctx, sys, st.PID, strings.Join(st.Args, " ")) || ourTemporal(ctx, sys, st, home) || ourListener(ctx, sys, st, home)) {
			homes = append(homes, home)
			continue
		}
		// Left by a heedle that was killed: it says nothing, so it goes,
		// and the UI's prune (which only looks for such files) isn't held
		// back by it (review 55).
		if err == nil && (st == nil || st.homeLabel(home) == e.Name()) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(homes)
	return homes
}

// otherHomes are the running homes but home.
func otherHomes(ctx context.Context, sys system, home string) []string {
	var out []string
	for _, h := range runningHomes(ctx, sys) {
		if h != home {
			out = append(out, h)
		}
	}
	return out
}
