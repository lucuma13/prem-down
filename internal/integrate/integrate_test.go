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
	if !strings.Contains(errw.String(), "unknown action --nope") {
		t.Errorf("missing diagnostic:\n%s", errw.String())
	}
	if !strings.Contains(errw.String(), "Usage: prem-down integrate") {
		t.Errorf("usage should go to the error stream:\n%s", errw.String())
	}
	if out.String() != "" {
		t.Errorf("nothing should be written to the output stream: %q", out.String())
	}
}

// Asking for both directions at once is a contradiction.
func TestIntegrateRunRejectsOnAndOffTogether(t *testing.T) {
	var out, errw strings.Builder
	if code := Run(&out, &errw, []string{"on", "off"}); code != 1 {
		t.Errorf("on off should return 1, got %d", code)
	}
	if !strings.Contains(errw.String(), "cannot be combined") {
		t.Errorf("missing diagnostic:\n%s", errw.String())
	}
	if out.String() != "" {
		t.Errorf("nothing should be written to the output stream: %q", out.String())
	}
}

// A bare `integrate` reports and changes nothing. Either state is a valid
// answer.
func TestIntegrateRunReportsStatus(t *testing.T) {
	var out, errw strings.Builder
	if code := Run(&out, &errw, nil); code != 0 {
		t.Fatalf("a bare integrate should return 0, got %d (err=%q)", code, errw.String())
	}
	got := strings.TrimSpace(out.String())
	if got != "integrate: on" && got != "integrate: off" {
		t.Errorf("status should report on or off, got %q", got)
	}
	if errw.String() != "" {
		t.Errorf("nothing should be written to the error stream: %q", errw.String())
	}
}

func TestUsageIntegrate(t *testing.T) {
	var b strings.Builder
	usageIntegrate(&b)
	got := b.String()
	for _, want := range []string{"Usage: prem-down integrate", "on", "off", "right-click"} {
		if !strings.Contains(got, want) {
			t.Errorf("usageIntegrate() output missing %q:\n%s", want, got)
		}
	}
}
