package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.2", "0.1", true},
		{"v0.2", "0.1", true}, // GitHub tags carry a leading v
		{"1.0", "0.9.9", true},
		{"0.1.1", "0.1", true}, // missing fields compare as zero
		{"0.1", "0.1", false},
		{"0.1", "0.1.0", false},
		{"0.1", "0.2", false},
		{"abc", "0.1", false}, // unparsable => stay quiet
		{"0.2", "unknown", false},
		{"0.2", "0.1-3-gabc-dirty", false}, // dev build => stay quiet
		{"", "0.1", false},
	}
	for _, c := range cases {
		if got := isNewer(c.latest, c.current); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestUpgradeHintForPath(t *testing.T) {
	githubTarget := "https://github.com/" + githubRepo + "/releases/latest"
	cases := []struct {
		exe        string
		wantVerb   string
		wantTarget string
	}{
		// The cask layout is what goreleaser actually publishes: <prefix>/bin is
		// a symlink into the Caskroom.
		{"/opt/homebrew/Caskroom/prem-down/0.2/prem-down", "Run", "brew upgrade prem-down"},
		{"/usr/local/Caskroom/prem-down/0.2/prem-down", "Run", "brew upgrade prem-down"},
		{"/opt/homebrew/Cellar/prem-down/0.2/bin/prem-down", "Run", "brew upgrade prem-down"},
		{"/usr/local/Cellar/prem-down/0.2/bin/prem-down", "Run", "brew upgrade prem-down"},
		{"/usr/local/bin/prem-down", "Download", githubTarget},
		{"/Users/x/Downloads/prem-down", "Download", githubTarget},
		// "Caskroom"/"Cellar" must be a whole path component, not a substring.
		{"/Users/x/MyCaskroomApp/prem-down", "Download", githubTarget},
		{"/Users/x/MyCellarApp/prem-down", "Download", githubTarget},
		{"", "Download", githubTarget},
	}
	for _, c := range cases {
		verb, target := upgradeHintForPath(c.exe)
		if verb != c.wantVerb || target != c.wantTarget {
			t.Errorf("upgradeHintForPath(%q) = (%q, %q), want (%q, %q)",
				c.exe, verb, target, c.wantVerb, c.wantTarget)
		}
	}
}

func TestCacheRoundTrip(t *testing.T) {
	n := newUpdateNotifier("0.1")
	n.cachePath = filepath.Join(t.TempDir(), "update-check.json")

	if _, ok := n.readCache(); ok {
		t.Fatal("readCache reported ok with no cache file")
	}
	n.writeCache("0.9")
	latest, ok := n.readCache()
	if !ok || latest != "0.9" {
		t.Fatalf("readCache = (%q, %v), want (\"0.9\", true)", latest, ok)
	}

	// An expired cache means "check again".
	n.checkInterval = 0
	if _, ok := n.readCache(); ok {
		t.Error("readCache reported ok for an expired cache")
	}

	// A CheckedAt in the future (clock skew) means "check again" too — it must
	// not count as fresh forever.
	n.checkInterval = updateCheckInterval
	future := fmt.Sprintf(`{"latest": "0.9", "checked_at": %d}`, time.Now().Add(time.Hour).Unix())
	if err := os.WriteFile(n.cachePath, []byte(future), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	if _, ok := n.readCache(); ok {
		t.Error("readCache reported ok for a future-dated cache")
	}

	// A corrupt cache means "check again", nothing more.
	n.checkInterval = updateCheckInterval
	if err := os.WriteFile(n.cachePath, []byte("not json"), 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	if _, ok := n.readCache(); ok {
		t.Error("readCache reported ok for a corrupt cache")
	}
}

func TestFetchStoresAndCachesLatest(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"tag_name": "v9.9.9"}`))
	}))
	defer srv.Close()

	n := newUpdateNotifier("0.1")
	n.apiURL = srv.URL
	n.cachePath = filepath.Join(t.TempDir(), "update-check.json")
	n.fetch()

	// GitHub rejects requests with no User-Agent; the fetch must send one.
	if gotUA == "" {
		t.Error("fetch sent no User-Agent header")
	}

	if n.latest != "v9.9.9" {
		t.Errorf("latest = %q, want \"v9.9.9\"", n.latest)
	}
	latest, ok := n.readCache()
	if !ok || latest != "v9.9.9" {
		t.Errorf("cache after fetch = (%q, %v), want (\"v9.9.9\", true)", latest, ok)
	}
}

func TestFetchFailuresAreSilent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer srv.Close()

	n := newUpdateNotifier("0.1")
	n.cachePath = filepath.Join(t.TempDir(), "update-check.json")

	for _, url := range []string{srv.URL, "http://127.0.0.1:1/nowhere"} {
		n.apiURL = url
		n.fetch()
		if n.latest != "" {
			t.Errorf("latest = %q after failed fetch from %s, want \"\"", n.latest, url)
		}
	}
	if _, ok := n.readCache(); ok {
		t.Error("failed fetch wrote a cache entry")
	}
}

// captureStderr runs f with os.Stderr redirected to a pipe and returns what it
// wrote.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	f()
	_ = w.Close()
	os.Stderr = orig
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestNotify(t *testing.T) {
	// A newer release with the fetch already settled prints the upgrade hint.
	t.Run("newer prints hint", func(t *testing.T) {
		n := newUpdateNotifier("0.1")
		n.latest = "0.2"
		n.hintVerb, n.hintTarget = "Run", "brew upgrade prem-down"
		done := make(chan struct{})
		close(done)
		n.done = done
		got := captureStderr(t, func() { n.notify(time.Second) })
		if !strings.Contains(got, "Update available") || !strings.Contains(got, "brew upgrade prem-down") {
			t.Errorf("notify output = %q, want the upgrade hint", got)
		}
	})

	// No newer release => silent, even with the fetch settled.
	t.Run("same version is silent", func(t *testing.T) {
		n := newUpdateNotifier("0.1")
		n.latest = "0.1"
		if got := captureStderr(t, func() { n.notify(time.Second) }); got != "" {
			t.Errorf("notify output = %q, want silence", got)
		}
	})

	// Empty latest (no result at all) => silent.
	t.Run("no result is silent", func(t *testing.T) {
		n := newUpdateNotifier("0.1")
		if got := captureStderr(t, func() { n.notify(time.Second) }); got != "" {
			t.Errorf("notify output = %q, want silence", got)
		}
	})

	// A fetch that misses the timeout window is abandoned silently, even though a
	// newer release would otherwise be announced.
	t.Run("timeout is silent", func(t *testing.T) {
		n := newUpdateNotifier("0.1")
		n.latest = "0.2"
		n.done = make(chan struct{}) // never closed
		got := captureStderr(t, func() { n.notify(10 * time.Millisecond) })
		if got != "" {
			t.Errorf("notify output = %q, want silence on timeout", got)
		}
	})
}

// A 200 response that isn't a usable release (malformed JSON, or an empty tag)
// must leave latest empty and write no cache — the fetch simply learned nothing.
func TestFetchIgnoresUnusableResponses(t *testing.T) {
	cases := map[string]string{
		"malformed json": `{not json`,
		"empty tag":      `{"tag_name": ""}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			n := newUpdateNotifier("0.1")
			n.apiURL = srv.URL
			n.cachePath = filepath.Join(t.TempDir(), "update-check.json")
			n.fetch()

			if n.latest != "" {
				t.Errorf("latest = %q, want empty", n.latest)
			}
			if _, ok := n.readCache(); ok {
				t.Error("an unusable response wrote a cache entry")
			}
		})
	}
}

// With no cache path (os.UserCacheDir unavailable) both cache ops are no-ops
// rather than errors.
func TestCacheNoPathIsNoOp(t *testing.T) {
	n := newUpdateNotifier("0.1")
	n.cachePath = ""
	if _, ok := n.readCache(); ok {
		t.Error("readCache reported ok with no cache path")
	}
	n.writeCache("0.9") // must not panic
}

// writeCache is best-effort: an un-creatable cache directory is swallowed. Here
// a parent path element is a regular file, so MkdirAll fails.
func TestWriteCacheDirFailureIsSilent(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil { //nolint:gosec // G306: test fixture file, perms irrelevant
		t.Fatal(err)
	}
	n := newUpdateNotifier("0.1")
	n.cachePath = filepath.Join(blocker, "sub", "update-check.json")
	n.writeCache("0.9") // MkdirAll fails; must be swallowed
	if _, ok := n.readCache(); ok {
		t.Error("writeCache into an un-creatable dir still produced a readable cache")
	}
}

// A dev build (version string that doesn't parse) opts out before the terminal
// check — we never nag someone running an unreleased binary.
func TestEnabledDisabledForDevBuild(t *testing.T) {
	for _, v := range []string{"PREM_DOWN_NO_UPDATE_CHECK", "NO_UPDATE_CHECK", "CI"} {
		t.Setenv(v, "") // ensure no opt-out short-circuits the check first
	}
	n := newUpdateNotifier("0.1-3-gabcdef-dirty")
	if parseVersion(n.currentVersion) != nil {
		t.Fatal("test precondition: version should be unparsable")
	}
	if n.enabled() {
		t.Error("enabled() true for a dev build, want disabled")
	}
}

func TestEnabledOptOuts(t *testing.T) {
	n := newUpdateNotifier("0.1")
	for _, v := range []string{"PREM_DOWN_NO_UPDATE_CHECK", "NO_UPDATE_CHECK", "CI"} {
		t.Setenv(v, "1")
		if n.enabled() {
			t.Errorf("enabled() with %s=1, want disabled", v)
		}
		t.Setenv(v, "")
		_ = os.Unsetenv(v)
	}
	// Even with no opt-out set, tests run with stderr redirected (not a
	// terminal), so the check stays disabled and start() is a no-op.
	if n.enabled() && !stderrIsTerminal() {
		t.Error("enabled() true although stderr is not a terminal")
	}
	n.start()
	if n.done != nil && stderrIsTerminal() == false {
		t.Error("start() spawned a fetch although disabled")
	}
}
