// Portable GitHub-release update checker.
//
// It checks GitHub for a newer release of the binary and prints an upgrade
// hint to stderr:
//
//	Update available! Download: https://github.com/<owner>/<repo>/releases/latest
//
// Features:
//   - the upgrade command is inferred from where the binary lives on disk
//     (a Homebrew install yields "brew upgrade prem-down"), defaulting to the
//     GitHub releases page when unrecognized
//   - never crashes or slows down the host CLI: every failure is silent, and
//     the network fetch runs in a goroutine that is simply abandoned if it
//     hasn't finished by the time notify() returns (the result still lands in
//     the cache for the next run)
//   - at most one GitHub request per checkInterval (result cached private to
//     the user in the platform cache dir; another fetch happens only once it
//     expires)
//   - the fetch identifies its sender in the User-Agent (GitHub rejects
//     requests that omit it) and caps the response it reads
//   - hints only appear on interactive runs (stderr is a terminal)
//   - opt out with PREM_DOWN_NO_UPDATE_CHECK=1, NO_UPDATE_CHECK=1, or in CI
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	githubRepo          = "Lucuma13/prem-down"
	updateCheckInterval = 24 * time.Hour
	updateNotifyTimeout = 250 * time.Millisecond
)

// parseVersion parses "1.2.3" (an optional leading "v" is accepted) into its
// numeric fields. nil means "cannot compare, stay quiet" — e.g. a dev build
// stamped by git describe ("v0.1-3-gabc123-dirty").
func parseVersion(s string) []int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || strings.ContainsAny(p, "+- ") {
			return nil
		}
		out[i] = n
	}
	return out
}

// isNewer reports whether latest is a strictly higher release than current.
// An unparsable version on either side yields false — a wrong hint is worse
// than no hint. Missing fields compare as zero ("1.2" == "1.2.0").
func isNewer(latest, current string) bool {
	lv, cv := parseVersion(latest), parseVersion(current)
	if lv == nil || cv == nil {
		return false
	}
	for i := 0; i < len(lv) || i < len(cv); i++ {
		var l, c int
		if i < len(lv) {
			l = lv[i]
		}
		if i < len(cv) {
			c = cv[i]
		}
		if l != c {
			return l > c
		}
	}
	return false
}

// stderrIsTerminal reports whether stderr is a terminal a human is looking at
// (character device, so false for pipes, files and CI logs).
func stderrIsTerminal() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// upgradeHintForPath returns the verb and target of the upgrade hint for a
// binary living at exe (already symlink-resolved). Each installer keeps its
// binaries in a recognizable place, so the on-disk location gives away who
// installed it: Homebrew stages casks under <prefix>/Caskroom/<token>/... —
// which is how goreleaser publishes this tool — and links formula kegs out of
// <prefix>/Cellar/<formula>/... Anything unrecognized falls back to the
// GitHub releases page — the manual install the README recommends.
func upgradeHintForPath(exe string) (verb, target string) {
	parts := strings.FieldsFunc(exe, func(r rune) bool { return r == '/' || r == '\\' })
	if slices.Contains(parts, "Caskroom") || slices.Contains(parts, "Cellar") {
		return "Run", "brew upgrade prem-down"
	}
	return "Download", "https://github.com/" + githubRepo + "/releases/latest"
}

func detectUpgradeHint() (verb, target string) {
	exe, err := os.Executable()
	if err != nil {
		return upgradeHintForPath("")
	}
	// Resolve symlinks: Homebrew invokes via <prefix>/bin/prem-down, a link
	// into the Caskroom.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return upgradeHintForPath(exe)
}

type updateNotifier struct {
	currentVersion string
	checkInterval  time.Duration
	apiURL         string // GitHub "latest release" endpoint; a field so tests can stub it
	cachePath      string
	hintVerb       string // "Run" or "Download"
	hintTarget     string // the command or URL
	latest         string
	done           chan struct{} // closed when the fetch goroutine finishes; nil if none started
}

func newUpdateNotifier(currentVersion string) *updateNotifier {
	n := &updateNotifier{
		currentVersion: currentVersion,
		checkInterval:  updateCheckInterval,
		apiURL:         "https://api.github.com/repos/" + githubRepo + "/releases/latest",
	}
	n.hintVerb, n.hintTarget = detectUpgradeHint()
	if dir, err := os.UserCacheDir(); err == nil {
		n.cachePath = filepath.Join(dir, "prem-down", "update-check.json")
	}
	return n
}

// start begins the check. Instant: either reads a fresh cache or spawns a
// goroutine.
func (n *updateNotifier) start() {
	if !n.enabled() {
		return
	}
	if cached, ok := n.readCache(); ok {
		n.latest = cached
		return
	}
	n.done = make(chan struct{})
	go func() {
		defer close(n.done)
		n.fetch()
	}()
}

// notify prints the upgrade hint to stderr if a newer release is known.
//
// Waits at most timeout for an in-flight fetch — long enough for a warm
// connection, short enough to be imperceptible. A fetch that misses the
// window still lands in the cache for the next run (unless the process exits
// first, which is also fine); n.latest is only read after done closes, so
// there is no race with the abandoned goroutine.
func (n *updateNotifier) notify(timeout time.Duration) {
	if n.done != nil {
		select {
		case <-n.done:
		case <-time.After(timeout):
			return
		}
	}
	if n.latest == "" || !isNewer(n.latest, n.currentVersion) {
		return
	}
	fmt.Fprintf(os.Stderr, "Update available! %s: %s\n", n.hintVerb, n.hintTarget)
}

func (n *updateNotifier) enabled() bool {
	// Any non-empty value opts out — including "0" and "false" — matching how
	// CI-style flags are conventionally treated.
	for _, v := range []string{"PREM_DOWN_NO_UPDATE_CHECK", "NO_UPDATE_CHECK", "CI"} {
		if os.Getenv(v) != "" {
			return false
		}
	}
	if parseVersion(n.currentVersion) == nil { // dev build / unknown version
		return false
	}
	return stderrIsTerminal()
}

// fetch asks GitHub for the latest release tag and caches it. Runs in the
// abandoned-on-timeout goroutine, so every failure is silent.
func (n *updateNotifier) fetch() {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, n.apiURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// GitHub rejects requests with no User-Agent header; identify the sender.
	req.Header.Set("User-Agent", "prem-down/"+n.currentVersion+" (update-check)")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return
	}
	if payload.TagName == "" {
		return
	}
	n.latest = payload.TagName
	n.writeCache(payload.TagName)
}

type updateCache struct {
	Latest    string `json:"latest"`
	CheckedAt int64  `json:"checked_at"`
}

// readCache returns the cached latest version, or ok=false if
// absent/stale/corrupt (a bad cache means "check again", nothing more).
func (n *updateNotifier) readCache() (latest string, ok bool) {
	if n.cachePath == "" {
		return "", false
	}
	raw, err := os.ReadFile(n.cachePath)
	if err != nil {
		return "", false
	}
	var c updateCache
	if err := json.Unmarshal(raw, &c); err != nil || c.Latest == "" {
		return "", false
	}
	// A negative age (CheckedAt in the future, i.e. clock skew) is treated as
	// stale — otherwise a skewed cache would count as fresh indefinitely.
	if age := time.Since(time.Unix(c.CheckedAt, 0)); age < 0 || age >= n.checkInterval {
		return "", false
	}
	return c.Latest, true
}

// writeCache is best-effort: write-then-rename so a concurrent reader never
// sees a torn file.
func (n *updateNotifier) writeCache(latest string) {
	if n.cachePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(n.cachePath), 0o755); err != nil {
		return
	}
	payload, err := json.Marshal(updateCache{Latest: latest, CheckedAt: time.Now().Unix()})
	if err != nil {
		return
	}
	// Sweep temp files orphaned by earlier runs that exited between write and
	// rename (this goroutine is abandoned once notify's timeout passes).
	if stale, err := filepath.Glob(n.cachePath + ".tmp.*"); err == nil {
		for _, s := range stale {
			os.Remove(s)
		}
	}
	tmp := fmt.Sprintf("%s.tmp.%d", n.cachePath, os.Getpid())
	// 0o600: the cache is private to the user, matching os.UserCacheDir intent.
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, n.cachePath); err != nil {
		os.Remove(tmp)
	}
}
