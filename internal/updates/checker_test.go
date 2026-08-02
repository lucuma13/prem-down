// Tests for the opt-in update checker. Every one of them runs against an
// httptest server and a settings file under t.TempDir, so the suite never
// reaches GitHub, never reads the real settings, and never raises a prompt.

package updates

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
	c.Announce = func(Upgrade) bool { return false }
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

// Comparable is the test the host applies to its own version string before
// reporting it, so it has to agree with the comparison Newer will make: exactly
// the versions Comparable accepts are the ones that can produce a notice.
func TestComparable(t *testing.T) {
	for _, v := range []string{"1.2.3", "v1.2.3", "1.2", "0"} {
		if !Comparable(v) {
			t.Errorf("Comparable(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "dev", "v0.1-3-gabc123-dirty", "1.2.3+dirty", "nightly"} {
		if Comparable(v) {
			t.Errorf("Comparable(%q) = true, want false", v)
		}
		// Nothing Comparable rejects may ever produce a notice.
		if Newer(v, "99.0.0") || Newer("1.0.0", v) {
			t.Errorf("%q is not comparable but Newer used it anyway", v)
		}
	}
}

// Without an override the request goes to GitHub's latest-release API for Repo,
// which is the one network endpoint this package ever contacts.
func TestEndpointDefaultsToGitHub(t *testing.T) {
	c := New("owner/repo", "my-tool", "1.0.0")
	if got, want := c.endpoint(), "https://api.github.com/repos/owner/repo/releases/latest"; got != want {
		t.Errorf("endpoint() = %q, want %q", got, want)
	}
	c.Endpoint = "http://localhost:1234"
	if got := c.endpoint(); got != "http://localhost:1234" {
		t.Errorf("an override should win, got %q", got)
	}
}

// An endpoint that cannot be turned into a request fails before anything is
// sent, and like every other request failure it is returned, never fatal.
func TestLatestRejectsAnUnusableEndpoint(t *testing.T) {
	c := New("owner/repo", "my-tool", "1.0.0")
	c.Endpoint = "http://host\x7f/releases" // a control character url.Parse refuses
	if _, err := c.latest(); err == nil {
		t.Error("want an error for an unparsable endpoint, got nil")
	}
}

// With no ConfigPath override the settings live in the per-user config
// directory under the product's own folder; a host that has no such directory
// is reported rather than guessed at, and every caller of configPath passes
// that failure on instead of silently reading or writing somewhere else.
func TestConfigPathDefaultsToTheUserConfigDir(t *testing.T) {
	c := New("owner/repo", "my-tool", "1.0.0")

	t.Setenv("HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir()) // Windows' equivalent
	t.Setenv("XDG_CONFIG_HOME", "")  // Unix: fall back to $HOME/.config
	path, err := c.configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if want := filepath.Join("my-tool", "config.json"); !strings.HasSuffix(path, want) {
		t.Errorf("configPath() = %q, want it to end in %q", path, want)
	}

	// No config directory at all: an error, and no fallback path.
	t.Setenv("HOME", "")
	t.Setenv("AppData", "")
	if _, err := c.configPath(); err == nil {
		t.Fatal("want an error when there is no user config directory, got nil")
	}
	if _, err := c.load(); err == nil {
		t.Error("load should pass the configPath failure on")
	}
	if err := c.save(settings{Updates: stateOn}); err == nil {
		t.Error("save should pass the configPath failure on")
	}
	var o, e bytes.Buffer
	if code := c.Command(&o, &e, nil); code != 1 || !strings.Contains(e.String(), "error:") {
		t.Errorf("the subcommand should report it: code=%d err=%q", code, e.String())
	}
}

// Every step of the atomic save is reported rather than half-done: no settings
// directory that can be created, no temp file that can be written, and no
// rename onto the target. The last one must also leave no temp file behind.
func TestSaveReportsEveryFailure(t *testing.T) {
	dir := t.TempDir()

	// The settings directory cannot be created: a regular file sits where it
	// would go.
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New("owner/repo", "my-tool", "1.0.0")
	c.ConfigPath = filepath.Join(blocker, "config.json")
	if err := c.save(settings{Updates: stateOn}); err == nil {
		t.Error("want an error when the settings directory cannot be created")
	}

	// The temp file cannot be written: its exact name is already a directory.
	c.ConfigPath = filepath.Join(dir, "taken", "config.json")
	tmp := fmt.Sprintf("%s.tmp.%d", c.ConfigPath, os.Getpid())
	if err := os.MkdirAll(tmp, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := c.save(settings{Updates: stateOn}); err == nil {
		t.Error("want an error when the temp file cannot be written")
	}

	// The rename cannot happen: the target is a non-empty directory. The
	// previous settings (such as they are) survive, and no temp file is left.
	c.ConfigPath = filepath.Join(dir, "occupied")
	if err := os.MkdirAll(filepath.Join(c.ConfigPath, "child"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := c.save(settings{Updates: stateOn}); err == nil {
		t.Error("want an error when the settings cannot be renamed into place")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("a failed rename left a temp file behind: %s", e.Name())
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
	if got.Updates != stateUnset {
		t.Errorf("missing file should read as unset, got %q", got.Updates)
	}

	want := settings{Updates: stateOn, LastChecked: time.Now().UTC().Truncate(time.Second), LatestSeen: "v1.1.0"}
	if err := c.save(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = c.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Updates != want.Updates || got.LatestSeen != want.LatestSeen || !got.LastChecked.Equal(want.LastChecked) {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, want)
	}

	if err := os.WriteFile(c.ConfigPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.load(); err == nil {
		t.Error("malformed settings should return an error")
	}

	// Only a missing file reads as "never asked"; a file that exists but cannot
	// be read is an error, so a settings file the user cannot read never looks
	// like consent to ask again.
	c.ConfigPath = t.TempDir()
	if _, err := c.load(); err == nil {
		t.Error("unreadable settings should return an error")
	}
}

// Save must not leave a partial file behind, and must not clobber the previous
// settings when the rename cannot happen.
func TestSaveLeavesNoTempFileBehind(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	if err := c.save(settings{Updates: stateOn}); err != nil {
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
// Notify - consent first, then the throttle, then the notice.
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
	if err != nil || s.Updates != stateOff {
		t.Errorf("want the answer persisted as off, got %+v (err %v)", s, err)
	}
}

// Accepting checks straight away - LastChecked starts zero - and reports the
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
	if s.Updates != stateOn || s.LatestSeen != "v1.1.0" || s.LastChecked.IsZero() {
		t.Errorf("want the check recorded, got %+v", s)
	}
}

// On a surface that could put the question, the notice goes to that surface's
// dialog rather than the writer.
func TestNotifyAnnouncesOnTheAskingSurface(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	c.Ask = func(string, io.Reader, io.Writer) bool { return true }
	var announced []Upgrade
	c.Announce = func(u Upgrade) bool {
		announced = append(announced, u)
		return true
	}

	if got := notify(c, true); got != "" {
		t.Errorf("an announced notice must not also be printed, got %q", got)
	}
	if len(announced) != 1 {
		t.Fatalf("want one announcement, got %d: %v", len(announced), announced)
	}
	// The dialog is handed the upgrade itself, not a sentence: it labels its
	// action button with the verb and runs the target when that button is
	// pressed, so both have to arrive intact.
	if got := announced[0]; got.Version != "v1.1.0" || got.Verb == "" || got.Target == "" {
		t.Errorf("announced upgrade is incomplete: %+v", got)
	}

	// A terminal run is never announced to, whatever the platform can raise.
	c2, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	if err := c2.save(settings{Updates: stateOn, LatestSeen: "v1.1.0"}); err != nil {
		t.Fatal(err)
	}
	c2.Announce = func(Upgrade) bool { t.Error("a terminal run must not raise a dialog"); return true }
	if got := notify(c2, false); !strings.Contains(got, "is available") {
		t.Errorf("a terminal run should print the notice, got %q", got)
	}
}

// A dialog that cannot be raised must not swallow the notice: announce reports
// that it did not show, and the notice is printed after all.
func TestNotifyPrintsWhenTheDialogFails(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	c.Ask = func(string, io.Reader, io.Writer) bool { return true }
	c.Announce = func(Upgrade) bool { return false }

	if got := notify(c, true); !strings.Contains(got, "is available") {
		t.Errorf("a failed announcement should fall back to printing, got %q", got)
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
	if err := c.save(settings{Updates: stateOn, LastChecked: time.Now().Add(24 * time.Hour)}); err != nil {
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
	if err := c.save(settings{Updates: stateOn}); err != nil {
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

// Unreadable settings mean silence - never a re-ask, never an unconsented
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
	if s, _ := c.load(); s.Updates != stateOn {
		t.Errorf("want the stdin answer honoured as on, got %+v", s)
	}
}

// The wording of the question and the gap between requests are the two things a
// host may reasonably want to phrase or pace itself; both defaults give way to
// the field.
func TestQuestionAndIntervalOverrides(t *testing.T) {
	c, hits := newTestChecker(t, "1.0.0", "v1.1.0")
	c.Question = "Look for updates?"
	c.Interval = time.Hour
	var asked string
	c.Ask = func(question string, _ io.Reader, _ io.Writer) bool { asked = question; return true }

	notify(c, true)
	if asked != c.Question {
		t.Errorf("want the configured question, got %q", asked)
	}
	if hits.Load() != 1 {
		t.Fatalf("want one request on opting in, got %d", hits.Load())
	}
	// Inside the configured hour: still throttled. Past it: checked again.
	c.Now = func() time.Time { return time.Now().Add(30 * time.Minute) }
	notify(c, false)
	if hits.Load() != 1 {
		t.Errorf("want the configured interval to throttle, got %d requests", hits.Load())
	}
	c.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	notify(c, false)
	if hits.Load() != 2 {
		t.Errorf("want a request once the configured interval has passed, got %d", hits.Load())
	}
}

// If the answer cannot be persisted, the run stops there: checking anyway would
// contact GitHub on a consent that will be asked for again next run.
func TestNotifyStopsWhenTheAnswerCannotBeSaved(t *testing.T) {
	c, hits := newTestChecker(t, "1.0.0", "v1.1.0")
	c.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	// The save writes through this temp name; make it a directory so it fails
	// while the (absent) settings file still reads as "never asked".
	if err := os.MkdirAll(fmt.Sprintf("%s.tmp.%d", c.ConfigPath, os.Getpid()), 0o750); err != nil {
		t.Fatal(err)
	}
	c.Ask = func(string, io.Reader, io.Writer) bool { return true }

	if got := notify(c, true); got != "" {
		t.Errorf("want no output, got %q", got)
	}
	if hits.Load() != 0 {
		t.Errorf("want no request when the consent could not be stored, got %d", hits.Load())
	}
}

// --------------------------------------------------------------------------
// Command - the terminal way in and out.
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
	if code != 0 || !strings.Contains(out, "Update checks are not set.") {
		t.Errorf("bare status: code=%d out=%q", code, out)
	}
	if !strings.Contains(out, "my-tool updates on/off") {
		t.Errorf("unset status should name how to set it, got %q", out)
	}
	if _, err := os.Stat(c.ConfigPath); !os.IsNotExist(err) {
		t.Error("status must not create the settings file")
	}

	if code, out, _ = run("on"); code != 0 || !strings.Contains(out, "Update checks are on.") {
		t.Errorf("on: code=%d out=%q", code, out)
	}
	if s, _ := c.load(); s.Updates != stateOn {
		t.Error("on should persist")
	}
	// A bare invocation must report the stored setting without changing it.
	if code, out, _ = run(); code != 0 || !strings.Contains(out, "Update checks are on.") {
		t.Errorf("status: code=%d out=%q", code, out)
	}

	if code, out, _ = run("off"); code != 0 || !strings.Contains(out, "Update checks are off.") {
		t.Errorf("off: code=%d out=%q", code, out)
	}
	if s, _ := c.load(); s.Updates != stateOff || s.LatestSeen != "" {
		t.Errorf("off should persist and drop any pending notice, got %+v", s)
	}

	if code, out, _ = run("--help"); code != 0 || !strings.Contains(out, "Usage: my-tool updates") {
		t.Errorf("--help: code=%d out=%q", code, out)
	}
	if code, _, errw := run("maybe"); code != 1 || !strings.Contains(errw, "unknown action") {
		t.Errorf("unknown action: code=%d err=%q", code, errw)
	}
}

// Each action reports what it changed. The four transitions read differently.
func TestCommandActionsReportWhatChanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		start  string
		action string
		want   string
	}{
		{"on from unset", stateUnset, "on", "Update checks are on."},
		{"on from off", stateOff, "on", "Update checks are on."},
		{"on when already on", stateOn, "on", "Update checks are already on."},
		{"off from on", stateOn, "off", "Update checks are off."},
		{"off when already off", stateOff, "off", "Update checks are already off."},
		{"off from unset", stateUnset, "off", "Update checks are off."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
			if tc.start != stateUnset {
				if err := c.save(settings{Updates: tc.start}); err != nil {
					t.Fatalf("seeding state: %v", err)
				}
			}
			var o, e bytes.Buffer
			if code := c.Command(&o, &e, []string{tc.action}); code != 0 {
				t.Fatalf("%s returned %d (%q)", tc.action, code, e.String())
			}
			// One line, matched whole: "already off" must not pass for "off".
			if got := strings.TrimSpace(o.String()); got != tc.want {
				t.Errorf("%s said %q, want %q", tc.action, got, tc.want)
			}
			want := stateOn
			if tc.action == "off" {
				want = stateOff
			}
			if s, _ := c.load(); s.Updates != want {
				t.Errorf("%s left the setting at %q, want %q", tc.action, s.Updates, want)
			}
		})
	}
}

// The host names the subcommand, so the help it prints has to use that name
// rather than this package's default, or it would tell the user to type
// something their program does not accept.
func TestCommandNameOverride(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	c.CommandName = "check-updates" // deliberately not DefaultCommandName
	var o, e bytes.Buffer
	if code := c.Command(&o, &e, []string{"--help"}); code != 0 {
		t.Fatalf("--help: code=%d", code)
	}
	if !strings.Contains(o.String(), "my-tool check-updates") {
		t.Errorf("help should use the configured command name:\n%s", o.String())
	}
}

// Turning the check on when the setting cannot be stored has to fail loudly:
// the user asked for a change and would otherwise be told it took effect.
func TestCommandReportsAFailedSave(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	if err := os.MkdirAll(fmt.Sprintf("%s.tmp.%d", c.ConfigPath, os.Getpid()), 0o750); err != nil {
		t.Fatal(err)
	}
	var o, e bytes.Buffer
	if code := c.Command(&o, &e, []string{"on"}); code != 1 || !strings.Contains(e.String(), "error:") {
		t.Errorf("want a reported error, code=%d out=%q err=%q", code, o.String(), e.String())
	}
}

// Status reports the setting and nothing else.
func TestCommandStatusReportsOnlyTheSetting(t *testing.T) {
	c, _ := newTestChecker(t, "1.0.0", "v1.1.0")
	checked := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := c.save(settings{Updates: stateOn, LastChecked: checked, LatestSeen: "v1.1.0"}); err != nil {
		t.Fatal(err)
	}
	var o, e bytes.Buffer
	if code := c.Command(&o, &e, nil); code != 0 {
		t.Fatalf("status: code=%d err=%q", code, e.String())
	}
	if got := strings.TrimSpace(o.String()); got != "Update checks are on." {
		t.Errorf("status = %q, want just the setting", got)
	}
	for _, leaked := range []string{"last checked", checked.Local().Format(time.RFC1123), "v1.1.0", c.ConfigPath} {
		if strings.Contains(o.String(), leaked) {
			t.Errorf("status leaked %q:\n%s", leaked, o.String())
		}
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
	if code := c.Command(&o, &e, nil); code != 1 || !strings.Contains(e.String(), "error:") {
		t.Errorf("want a reported error, code=%d err=%q", code, e.String())
	}
}

// A build whose version cannot be compared - an unstamped `go build`, or the
// `git describe` string a working-tree build carries - is inert: no question,
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
				if err := c.save(settings{Updates: tc.state}); err != nil {
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
	if err := c.save(settings{Updates: stateOn, LastChecked: c.now(), LatestSeen: "v1.0.0"}); err != nil {
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
	if err := c.save(settings{Updates: stateOn}); err != nil {
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
	if err := c.save(settings{Updates: stateOn, LatestSeen: "v1.2.0"}); err != nil {
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
	if err := c.save(settings{Updates: stateOn}); err != nil {
		t.Fatal(err)
	}
	if u := c.CheckNow(); u != nil || hits.Load() != 0 {
		t.Errorf("want silence for a dev build, got %+v after %d requests", u, hits.Load())
	}
}
