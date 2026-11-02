package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestInv_CIChecksCanFail: every workflow runs steps with bash -eo
// pipefail (shell: bash), so a failing command in a pipe fails its step,
// and no command is negated ("! grep ..."), which errexit ignores.
func TestInv_CIChecksCanFail(t *testing.T) {
	files, _ := filepath.Glob(".github/workflows/*.yml")
	negated := regexp.MustCompile(`(^|&&|\|\||;|\bthen|\bdo)\s*!\s*[A-Za-z]`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "\ndefaults:\n  run:\n") || !strings.Contains(string(b), "\n    shell: bash\n") {
			t.Errorf("%s: no defaults run shell: bash, so pipes hide failures", f)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if l := strings.TrimSpace(line); !strings.HasPrefix(l, "#") && negated.MatchString(l) {
				t.Errorf("%s:%d: a negated command never fails a step: %s", f, i+1, l)
			}
		}
	}
}
