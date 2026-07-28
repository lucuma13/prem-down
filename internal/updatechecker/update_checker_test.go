// Tests for the opt-in update checker. Every one of them runs against an
// httptest server and a settings file under t.TempDir, so the suite never
// reaches GitHub, never reads the real settings, and never raises a prompt.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package updatechecker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestChecker builds a Checker pointed at a temp settings file and a stub
// server that answers with tag, counting the requests it receives.
func newTestChecker(t *testing.T, version, tag string) (*Checker, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// GitHub rejects requests without a User-Agent; make sure one is sent.
		if r.Header.Get("User-Agent") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": tag})
	}))
	t.Cleanup(srv.Close)

	c := New("owner/repo", "my-tool", version)
	c.Endpoint = srv.URL
	c.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	c.Ask = func(string, io.Reader, io.Writer) bool { return false }
	return c, &hits
}

// notify drives Notify over buffers and hands back what the user would see.
func notify(c *Checker, mayAsk bool) string {
	var out bytes.Buffer
	c.Notify(&out, strings.NewReader(""), mayAsk)
	return out.String()
}

func TestNewerAndParseVersion(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.0.0", "v1.1.0", true},
		{"v1.9.0", "1.10.0", true}, // numeric, not lexical
		{"1.2", "1.2.0", false},    // missing fields are zero
		{"1.2.0", "1.2", false},
		{"1.0.1", "1.0.0", false},
		{"1.0.0", "1.0.0", false},
		// Unparsable on either side must stay quiet: a dev build stamped by
		// git describe has no meaningful release to compare against.
		{"v0.1-3-gabc123-dirty", "1.0.0", false},
		{"dev", "1.0.0", false},
		{"1.0.0", "nightly", false},
		{"", "1.0.0", false},
	}
	for _, tc := range cases {
		if got := Newer(tc.current, tc.latest); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestUpgradeHintForPath(t *testing.T) {
	c := New("lucuma13/prem-down", "prem-down", "1.0.0")
	cases := []struct{ exe, wantVerb, wantTarget string }{
		{"/opt/homebrew/Caskroom/prem-down/1.0.0/prem-down", "Run", "brew upgrade prem-down"},
		{"/usr/local/Cellar/prem-down/1.0.0/bin/prem-down", "Run", "brew upgrade prem-down"},
		{`C:\Users\x\AppData\Local\Microsoft\WinGet\Packages\p\prem-down.exe`, "Run", "winget upgrade -e --id lucuma13.prem-down"},
		// Anything unrecognised (the .pkg and .msi installs) gets the page.
		{"/usr/local/bin/prem-down", "Download", "https://github.com/lucuma13/prem-down/releases/latest"},
		{"", "Download", "https://github.com/lucuma13/prem-down/releases/latest"},
	}
	for _, tc := range cases {
		verb, target := c.upgradeHintForPath(tc.exe)
		if verb != tc.wantVerb || target != tc.wantTarget {
			t.Errorf("upgradeHintForPath(%q) = %q, %q; want %q, %q", tc.exe, verb, target, tc.wantVerb, tc.wantTarget)
		}
	}
}

// A missing settings file is the normal pre-consent state and reads as unset,
// not as an error; a malformed one is an error.
func TestLoadSaveRoundTrip(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")

	got, err := c.load()
	if err != nil {
		t.Fatalf("load of a missing file should succeed, got %v", err)
	}
	if got.AutoUpdate != stateUnset {
		t.Errorf("missing file should read as unset, got %q", got.AutoUpdate)
	}

	want := settings{AutoUpdate: stateOn, LastChecked: time.Now().UTC().Truncate(time.Second), LatestSeen: "v1.1.0"}
	if err := c.save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = c.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.AutoUpdate != want.AutoUpdate || got.LatestSeen != want.LatestSeen || !got.LastChecked.Equal(want.LastChecked) {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, want)
	}

	if err := os.WriteFile(c.ConfigPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.load(); err == nil {
		t.Error("malformed settings should return an error")
	}
}

// Save must not leave a partial file behind, and must not clobber the previous
// settings when the rename cannot happen.
func TestSaveLeavesNoTempFileBehind(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	if err := c.save(settings{AutoUpdate: stateOn}); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(c.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("save left a temp file behind: %s", e.Name())
		}
	}
}

func TestLatestRejectsBadResponses(t *testing.T) {
	cases := []struct {
		name string
		h    http.HandlerFunc
	}{
		{"non-200", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }},
		{"empty tag", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"tag_name":""}`) }},
		{"malformed", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{oops`) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.h)
			defer srv.Close()
			c := New("owner/repo", "my-tool", "1.0.0")
			c.Endpoint = srv.URL
			if _, err := c.latest(); err == nil {
				t.Error("want an error, got nil")
			}
		})
	}
}

// --------------------------------------------------------------------------
// Notify — consent first, then the throttle, then the notice.
// --------------------------------------------------------------------------

// Without a surface to ask on, an unset setting stays unset: no question, no
// request, and nothing written that would count as an answer.
func TestNotifyStaysSilentWhenItCannotAsk(t *testing.T) {
	c, hits := newTestChecker(t, "1.0.0", "v1.1.0")
	c.Ask = func(string, io.Reader, io.Writer) bool {
		t.Error("Notify must not ask when mayAsk is false")
		return true
	}
	if got := notify(c, false); got != "" {
		t.Errorf("want no output, got %q", got)
	}
	if hits.Load() != 0 {
		t.Errorf("want no request, got %d", hits.Load())
	}
	if _, err := os.Stat(c.ConfigPath); !os.IsNotExist(err) {
		t.Error("declining to ask must not write a settings file")
	}
}

// Declining is remembered, so the question is asked exactly once ever.
func TestNotifyRemembersNo(t *testing.T) {
	c, hits := newTestChecker(t, "1.0.0", "v1.1.0")
	var asked int
	c.Ask = func(string, io.Reader, io.Writer) bool { asked++; return false }

	if got := notify(c, true); got != "" {
		t.Errorf("a declined check must print nothing, got %q", got)
	}
	notify(c, true)
	notify(c, true)
	if asked != 1 {
		t.Errorf("want the question asked once, asked %d times", asked)
	}
	if hits.Load() != 0 {
		t.Errorf("want no request after declining, got %d", hits.Load())
	}
	s, err := c.load()
	if err != nil || s.AutoUpdate != stateOff {
		t.Errorf("want the answer persisted as off, got %+v (err %v)", s, err)
	}
}

// Accepting checks straight away — LastChecked starts zero — and reports the
// newer release with the upgrade hint.
func TestNotifyAcceptsAndReports(t *testing.T) {
	c, hits := newTestChecker(t, "1.0.0", "v1.1.0")
	var asked int
	c.Ask = func(string, io.Reader, io.Writer) bool { asked++; return true }

	got := notify(c, true)
	if asked != 1 {
		t.Errorf("want the question asked once, asked %d times", asked)
	}
	if hits.Load() != 1 {
		t.Errorf("want one request on opting in, got %d", hits.Load())
	}
	for _, want := range []string{"my-tool", "v1.1.0", "is available"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice missing %q:\n%s", want, got)
		}
	}
	s, _ := c.load()
	if s.AutoUpdate != stateOn || s.LatestSeen != "v1.1.0" || s.LastChecked.IsZero() {
		t.Errorf("want the check recorded, got %+v", s)
	}
}

// The request is throttled but the notice is not: a pending upgrade keeps being
// reported from the cached version while GitHub is left alone.
func TestNotifyThrottlesRequestsNotNotices(t *testing.T) {
	c, hits := newTestChecker(t, "1.0.0", "v1.1.0")
	c.Ask = func(string, io.Reader, io.Writer) bool { return true }

	if got := notify(c, true); !strings.Contains(got, "v1.1.0") {
		t.Fatalf("first run should report the upgrade, got %q", got)
	}
	for i := 0; i < 3; i++ {
		if got := notify(c, false); !strings.Contains(got, "v1.1.0") {
			t.Errorf("run %d should still report the upgrade, got %q", i+2, got)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("want the request throttled to 1, got %d", hits.Load())
	}

	// Past the interval, one more request goes out.
	c.Now = func() time.Time { return time.Now().Add(DefaultInterval + time.Minute) }
	notify(c, false)
	if hits.Load() != 2 {
		t.Errorf("want a second request once stale, got %d", hits.Load())
	}
}

// A clock jumping backwards must not freeze the check forever.
func TestNotifyTreatsClockSkewAsStale(t *testing.T) {
	c, hits := newTestChecker(t, "1.0.0", "v1.1.0")
	if err := c.save(settings{AutoUpdate: stateOn, LastChecked: time.Now().Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	notify(c, false)
	if hits.Load() != 1 {
		t.Errorf("a future LastChecked should read as stale, got %d requests", hits.Load())
	}
}

// An offline run retries next time rather than burning the whole interval.
func TestNotifyFailedRequestDoesNotStartTheClock(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	c.Endpoint = "http://127.0.0.1:0" // nothing listening
	if err := c.save(settings{AutoUpdate: stateOn}); err != nil {
		t.Fatal(err)
	}
	if got := notify(c, false); got != "" {
		t.Errorf("a failed request must print nothing, got %q", got)
	}
	s, _ := c.load()
	if !s.LastChecked.IsZero() {
		t.Error("a failed request must leave LastChecked unset so the next run retries")
	}
}

// Being up to date, or ahead of the latest release, prints nothing.
func TestNotifyQuietWhenCurrent(t *testing.T) {
	for _, version := range []string{"1.1.0", "2.0.0"} {
		c, _ := newTestChecker(t, version, "v1.1.0")
		c.Ask = func(string, io.Reader, io.Writer) bool { return true }
		if got := notify(c, true); got != "" {
			t.Errorf("version %s should print nothing, got %q", version, got)
		}
	}
}

// Unreadable settings mean silence — never a re-ask, never an unconsented
// request, and never an error on top of a run that already succeeded.
func TestNotifyIgnoresUnreadableSettings(t *testing.T) {
	c, hits := newTestChecker(t, "1.0.0", "v1.1.0")
	if err := os.WriteFile(c.ConfigPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.Ask = func(string, io.Reader, io.Writer) bool {
		t.Error("unreadable settings must not trigger a question")
		return true
	}
	if got := notify(c, true); got != "" {
		t.Errorf("want no output, got %q", got)
	}
	if hits.Load() != 0 {
		t.Errorf("want no request, got %d", hits.Load())
	}
}

// The platform prompt gets the streams and its answer is honoured, which is
// what the Windows console prompt relies on.
func TestNotifyPassesStreamsToAsk(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	c.Ask = func(question string, in io.Reader, out io.Writer) bool {
		if !strings.Contains(question, "my-tool") {
			t.Errorf("question should name the product, got %q", question)
		}
		answer, _ := io.ReadAll(in)
		_, _ = fmt.Fprint(out, "asked")
		return strings.TrimSpace(string(answer)) == "y"
	}
	var out bytes.Buffer
	c.Notify(&out, strings.NewReader("y\n"), true)
	if !strings.Contains(out.String(), "asked") {
		t.Errorf("Ask should be able to write to out, got %q", out.String())
	}
	if s, _ := c.load(); s.AutoUpdate != stateOn {
		t.Errorf("want the stdin answer honoured as on, got %+v", s)
	}
}

// --------------------------------------------------------------------------
// Command — the terminal way in and out.
// --------------------------------------------------------------------------

func TestCommand(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	run := func(args ...string) (code int, out, errw string) {
		var o, e bytes.Buffer
		code = c.Command(&o, &e, args)
		return code, o.String(), e.String()
	}

	// A bare invocation reports and changes nothing.
	code, out, _ := run()
	if code != 0 || !strings.Contains(out, "not set") {
		t.Errorf("bare status: code=%d out=%q", code, out)
	}
	if _, err := os.Stat(c.ConfigPath); !os.IsNotExist(err) {
		t.Error("status must not create the settings file")
	}

	if code, out, _ = run("on"); code != 0 || !strings.Contains(out, "auto-update: on") {
		t.Errorf("on: code=%d out=%q", code, out)
	}
	if s, _ := c.load(); s.AutoUpdate != stateOn {
		t.Error("on should persist")
	}
	// status must report the stored setting without changing it.
	if code, out, _ = run("status"); code != 0 || !strings.Contains(out, "auto-update: on") {
		t.Errorf("status: code=%d out=%q", code, out)
	}
	if !strings.Contains(out, c.ConfigPath) {
		t.Errorf("status should name the settings file, got %q", out)
	}

	if code, out, _ = run("off"); code != 0 || !strings.Contains(out, "auto-update: off") {
		t.Errorf("off: code=%d out=%q", code, out)
	}
	if s, _ := c.load(); s.AutoUpdate != stateOff || s.LatestSeen != "" {
		t.Errorf("off should persist and drop any pending notice, got %+v", s)
	}

	if code, out, _ = run("--help"); code != 0 || !strings.Contains(out, "Usage: my-tool auto-update") {
		t.Errorf("--help: code=%d out=%q", code, out)
	}
	if code, _, errw := run("maybe"); code != 1 || !strings.Contains(errw, "unknown action") {
		t.Errorf("unknown action: code=%d err=%q", code, errw)
	}
}

// An explicit command reports a broken settings file instead of swallowing it,
// unlike the post-run path.
func TestCommandReportsUnreadableSettings(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	if err := os.WriteFile(c.ConfigPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var o, e bytes.Buffer
	if code := c.Command(&o, &e, []string{"status"}); code != 1 || !strings.Contains(e.String(), "error:") {
		t.Errorf("want a reported error, code=%d err=%q", code, e.String())
	}
}

// A build whose version cannot be compared — an unstamped `go build`, or the
// `git describe` string a working-tree build carries — is inert: no question,
// no request, no notice. Nothing useful could come of one, and a developer
// building off their own tree is already at or past the release they'd be told
// to install.
func TestNotifyDoesNothingForUnComparableVersions(t *testing.T) {
	for _, version := range []string{"dev", "0.1.0-33-g5f95cd1-dirty", ""} {
		t.Run(version, func(t *testing.T) {
			c, hits := newTestChecker(t, version, "v9.9.9")
			c.Ask = func(string, io.Reader, io.Writer) bool {
				t.Error("a dev build must not raise the question")
				return true
			}
			if got := notify(c, true); got != "" {
				t.Errorf("want no output, got %q", got)
			}
			if hits.Load() != 0 {
				t.Errorf("want no request, got %d", hits.Load())
			}
			if _, err := os.Stat(c.ConfigPath); !os.IsNotExist(err) {
				t.Error("a dev build must not write a settings file")
			}
		})
	}
}

// CheckNow answers the host's "is there a newer release that might handle this?"
// It is opt-in only and never asks, so an un-answered or declined setting is
// silence and no request.
func TestCheckNowRequiresOptIn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
	}{
		{"never asked", stateUnset},
		{"declined", stateOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, hits := newTestChecker(t, "1.0.0", "v1.1.0")
			c.Ask = func(string, io.Reader, io.Writer) bool {
				t.Error("CheckNow must never raise the question")
				return true
			}
			if tc.state != stateUnset {
				if err := c.save(settings{AutoUpdate: tc.state}); err != nil {
					t.Fatal(err)
				}
			}
			if u := c.CheckNow(); u != nil {
				t.Errorf("want no upgrade without opt-in, got %+v", u)
			}
			if hits.Load() != 0 {
				t.Errorf("want no request without opt-in, got %d", hits.Load())
			}
		})
	}
}

// Opted in, CheckNow reports the upgrade and the channel-specific way to take
// it, and ignores the throttle: the weekly gap serves the routine notice, but
// this caller is holding a file it could not identify, and a cached "nothing
// new" from six days ago is the wrong answer to give it.
func TestCheckNowIgnoresTheThrottle(t *testing.T) {
	c, hits := newTestChecker(t, "1.0.0", "v1.1.0")
	if err := c.save(settings{AutoUpdate: stateOn, LastChecked: c.now(), LatestSeen: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}

	u := c.CheckNow()
	if u == nil {
		t.Fatal("want an upgrade reported, got nil")
	}
	if u.Version != "v1.1.0" || u.Verb == "" || u.Target == "" {
		t.Errorf("incomplete upgrade: %+v", u)
	}
	if hits.Load() != 1 {
		t.Errorf("want a fresh request despite the recent check, got %d", hits.Load())
	}
	if s, _ := c.load(); s.LatestSeen != "v1.1.0" {
		t.Errorf("want the result recorded for later runs, got %+v", s)
	}
}

// Already current: nothing to say.
func TestCheckNowQuietWhenCurrent(t *testing.T) {
	c, _ := newTestChecker(t, "1.1.0", "v1.1.0")
	if err := c.save(settings{AutoUpdate: stateOn}); err != nil {
		t.Fatal(err)
	}
	if u := c.CheckNow(); u != nil {
		t.Errorf("want silence when current, got %+v", u)
	}
}

// A failed request falls back to the last version seen rather than reporting
// nothing: an offline machine should still say what it knew.
func TestCheckNowFallsBackToTheCachedVersion(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	c.Endpoint = "http://127.0.0.1:1/dead"
	if err := c.save(settings{AutoUpdate: stateOn, LatestSeen: "v1.2.0"}); err != nil {
		t.Fatal(err)
	}
	u := c.CheckNow()
	if u == nil || u.Version != "v1.2.0" {
		t.Errorf("want the cached upgrade when the request fails, got %+v", u)
	}
}

// A dev build has no comparison to make, so the feature is inert here too.
func TestCheckNowDoesNothingForUnComparableVersions(t *testing.T) {
	c, hits := newTestChecker(t, "1.2.3-4-gabc1234", "v9.9.9")
	if err := c.save(settings{AutoUpdate: stateOn}); err != nil {
		t.Fatal(err)
	}
	if u := c.CheckNow(); u != nil || hits.Load() != 0 {
		t.Errorf("want silence for a dev build, got %+v after %d requests", u, hits.Load())
	}
}
