// Package updates is a portable, opt-in GitHub release update checker.
//
// It asks once whether to look for new releases, remembers the answer forever,
// and - if the answer was yes - contacts GitHub's "latest release" API to
// report that a newer version exists. It is notify-only: a one-line notice
// naming the right upgrade command is the whole feature.
package updates

import (
	"encoding/json"
	"errors"
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

// The three values of settings.Updates. Unset is distinct from Off so the
// first-run question can be asked exactly once: "never asked" and "asked, said
// no" have to be told apart, or a user who declines gets asked forever.
const (
	stateUnset = ""
	stateOn    = "on"
	stateOff   = "off"
)

// DefaultInterval is the minimum gap between two requests. The notice itself is
// not throttled: the version last seen is cached, so a pending upgrade is
// reported on every run until it is taken.
const DefaultInterval = 7 * 24 * time.Hour

// DefaultCommandName is the subcommand Command implements, used in its own
// help text.
const DefaultCommandName = "updates"

// The two upgrade verbs. They say what kind of thing Target is - which is what
// every caller branches on to decide whether it can be executed.
const (
	verbRun      = "Run"      // Target is a command to execute
	verbDownload = "Download" // Target is a page to open
)

// requestTimeout bounds the whole check. The real work is already done by the
// time this runs, so the only thing at stake is how long the console window (or
// the desktop notification) is held back; three seconds is short enough that a
// dead network is indistinguishable from no check at all.
//
// The request is synchronous on purpose. An abandon-on-timeout goroutine would
// be cheaper, but the answer has to reach the same window that reports the
// run's result, and the process exits straight after - there is no later run
// for a late answer to land in.
const requestTimeout = 3 * time.Second

// Checker is the whole feature: consent, throttle, request and notice. Repo,
// Product and Version are required; every other field has a working default,
// and exists so tests (and unusual hosts) can substitute their own.
type Checker struct {
	Repo    string // GitHub "owner/repo" to read releases from
	Product string // program name: settings directory, upgrade hints, messages
	Version string // this build's version, as reported by --version

	ConfigPath  string                                                  // "" => <os.UserConfigDir>/<Product>/config.json
	Endpoint    string                                                  // "" => GitHub's latest-release API for Repo
	Client      *http.Client                                            // nil => a client bounded by requestTimeout
	Interval    time.Duration                                           // 0 => DefaultInterval
	CommandName string                                                  // "" => DefaultCommandName
	Question    string                                                  // "" => a default phrased around Product
	Now         func() time.Time                                        // nil => time.Now
	Ask         func(question string, in io.Reader, out io.Writer) bool // nil => the platform prompt
	Announce    func(u Upgrade) bool                                    // nil => the platform notice dialog
}

// New builds a Checker with every default in place.
func New(repo, product, version string) *Checker {
	return &Checker{Repo: repo, Product: product, Version: version}
}

// settings is the persisted state, and the only state this package keeps
// between runs: what the user answered, when the last request went out, and
// what it returned.
type settings struct {
	Updates string `json:"updates"`
	// omitzero, not omitempty: encoding/json's omitempty has no effect on a
	// struct, so a never-checked zero time would otherwise be written out as
	// "0001-01-01T00:00:00Z" in a file the user is invited to look at.
	LastChecked time.Time `json:"last_checked,omitzero"`
	LatestSeen  string    `json:"latest_seen,omitempty"`
}

// configPath is where the settings live: ~/Library/Application Support on
// macOS, %AppData% on Windows and $XDG_CONFIG_HOME on Unix, all by way of
// os.UserConfigDir, which is the conventional per-user location on each.
func (c *Checker) configPath() (string, error) {
	if c.ConfigPath != "" {
		return c.ConfigPath, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, c.Product, "config.json"), nil
}

// load returns the stored settings. A missing file is not an error - it is the
// normal state before the question has ever been asked, and reads as the zero
// value (stateUnset). Anything else is returned as an error, and Notify treats
// that as "do nothing": settings that cannot be read must never cause a re-ask,
// or a request the user did not agree to.
func (c *Checker) load() (settings, error) {
	path, err := c.configPath()
	if err != nil {
		return settings{}, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is this package's own, not user input
	if errors.Is(err, os.ErrNotExist) {
		return settings{}, nil
	}
	if err != nil {
		return settings{}, err
	}
	var s settings
	if err := json.Unmarshal(data, &s); err != nil {
		return settings{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// save writes the settings, creating the directory on first use. The write goes
// to a temp file that is renamed over the target, so a reader never sees a torn
// file and a failed write leaves the previous settings intact; the partial file
// is removed.
func (c *Checker) save(s settings) error {
	path, err := c.configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// 0o600: these settings are private to the user, matching os.UserConfigDir.
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// endpoint is GitHub's "latest release" API for Repo.
func (c *Checker) endpoint() string {
	if c.Endpoint != "" {
		return c.Endpoint
	}
	return "https://api.github.com/repos/" + c.Repo + "/releases/latest"
}

// releasesPage is the fallback upgrade target: the manual download, used when
// the install channel cannot be identified.
func (c *Checker) releasesPage() string {
	return "https://github.com/" + c.Repo + "/releases/latest"
}

// latest returns the tag of the newest published release.
func (c *Checker) latest() (string, error) {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	req, err := http.NewRequest(http.MethodGet, c.endpoint(), nil)
	if err != nil {
		return "", err
	}
	// GitHub rejects requests with no User-Agent header; identify the sender.
	req.Header.Set("User-Agent", c.Product+"/"+c.Version+" (update-check)")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", c.endpoint(), resp.Status)
	}
	// Cap the read: the endpoint is trusted, but a hung or hostile proxy should
	// not be able to stream unbounded data into a tool the user is waiting on.
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	if payload.TagName == "" {
		return "", errors.New("release response carried no tag_name")
	}
	return payload.TagName, nil
}

// parseVersion parses "1.2.3" (an optional leading "v" is accepted) into its
// numeric fields. nil means "cannot compare, stay quiet" - e.g. a dev build
// stamped by git describe ("v0.1-3-gabc123-dirty") or the literal "dev".
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

// Comparable reports whether a version string is one this package can compare:
// a plain numeric release like "1.2.3" or "v1.2.3". Anything else - "dev", a
// `git describe` string, a module pseudo-version - is a development build, and
// a Checker holding one stays silent rather than guess at a comparison.
//
// Exported so a host deciding which version string to report can apply exactly
// the test the checker will apply to it, instead of a second rule that could
// drift from this one.
func Comparable(version string) bool { return parseVersion(version) != nil }

// Newer reports whether latest is a strictly higher release than current. An
// unparsable version on either side yields false. Missing fields compare as
// zero ("1.2" == "1.2.0").
func Newer(current, latest string) bool {
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

// upgradeHintForPath returns the verb and target of the upgrade hint for a
// binary living at exe (already symlink-resolved). Each installer keeps its
// binaries in a recognizable place, so the on-disk location gives away who
// installed it: Homebrew stages casks under <prefix>/Caskroom/<token>/... and
// links formula kegs out of <prefix>/Cellar/<formula>/...; winget unpacks into
// a WinGet\Packages tree. Anything unrecognized falls back to the releases
// page, which is what installer-package users want anyway.
//
// One binary serves every channel, so this cannot be resolved at build time,
// and guessing wrong costs only a less convenient suggestion.
func (c *Checker) upgradeHintForPath(exe string) (verb, target string) {
	parts := strings.FieldsFunc(exe, func(r rune) bool { return r == '/' || r == '\\' })
	owner, _, _ := strings.Cut(c.Repo, "/")
	switch {
	case slices.Contains(parts, "Caskroom") || slices.Contains(parts, "Cellar"):
		return verbRun, "brew upgrade " + c.Product
	case slices.Contains(parts, "WinGet"):
		return verbRun, "winget upgrade -e --id " + strings.ToLower(owner) + "." + c.Product
	}
	return verbDownload, c.releasesPage()
}

func (c *Checker) upgradeHint() (verb, target string) {
	exe, err := os.Executable()
	if err != nil {
		return c.upgradeHintForPath("")
	}
	// Resolve symlinks: Homebrew invokes via <prefix>/bin/<product>, a link
	// into the Caskroom.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return c.upgradeHintForPath(exe)
}
