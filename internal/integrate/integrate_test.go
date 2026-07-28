// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package integrate

import (
	"strings"
	"testing"
)

// An unrecognised option is refused before anything is installed or removed:
// the usage goes to the error stream, the diagnostic names the option, and the
// exit code is 1.
func TestIntegrateRunRejectsUnknownOptions(t *testing.T) {
	var out, errw strings.Builder
	if code := Run(&out, &errw, []string{"--nope"}); code != 1 {
		t.Errorf("unknown option should return 1, got %d", code)
	}
	if !strings.Contains(errw.String(), "unknown option --nope") {
		t.Errorf("missing diagnostic:\n%s", errw.String())
	}
	if !strings.Contains(errw.String(), "Usage: prem-down integrate") {
		t.Errorf("usage should go to the error stream:\n%s", errw.String())
	}
	if out.String() != "" {
		t.Errorf("nothing should be written to the output stream: %q", out.String())
	}
}

func TestUsageIntegrate(t *testing.T) {
	var b strings.Builder
	usageIntegrate(&b)
	got := b.String()
	for _, want := range []string{"Usage: prem-down integrate", "--remove", "right-click"} {
		if !strings.Contains(got, want) {
			t.Errorf("usageIntegrate() output missing %q:\n%s", want, got)
		}
	}
}
