// The runtime half of the check: the first-run question, the throttle, and the
// notice itself.

package updates

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// question is the first-run prompt: one line, phrased around Product..
func (c *Checker) question() string {
	if c.Question != "" {
		return c.Question
	}
	return fmt.Sprintf("Do you want %s to check for updates automatically?", c.Product)
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) interval() time.Duration {
	if c.Interval > 0 {
		return c.Interval
	}
	return DefaultInterval
}

func (c *Checker) prompt(in io.Reader, out io.Writer) bool {
	if c.Ask != nil {
		return c.Ask(c.question(), in, out)
	}
	return c.ask(c.question(), in, out)
}

// announce offers the upgrade to the platform's dialog, reporting whether it
// was shown. False means the caller must print it instead - either because this
// platform shows the run's output some other way, or because the dialog could
// not be raised.
func (c *Checker) announce(u Upgrade) bool {
	if c.Announce != nil {
		return c.Announce(u)
	}
	return c.announceDialog(u)
}

// noticeText is the upgrade notice: what's new, and how to update.
func (c *Checker) noticeText(u Upgrade) string {
	target := u.Target
	if u.Verb == verbRun {
		target = "'" + target + "'"
	}
	return fmt.Sprintf("%s %s is available.\n%s: %s", c.Product, u.Version, u.Verb, target)
}

// plainVersion renders a version bare, without the "v" a release tag may carry.
func plainVersion(version string) string {
	return strings.TrimPrefix(version, "v")
}

// Notify is called once per run, after the host program's real work has
// succeeded, and reports a newer release on out.
//
// mayAsk says this run is attached to a surface where a question can be put to
// the user and answered - for a file-manager integration, a desktop dialog or a
// console it already owns. The first-run question is only asked there:
// those are the surfaces where the user has no other way to discover the
// setting, and the ones with somewhere to put a question. A plain terminal run
// stays silent and scriptable; that user has the updates subcommand and
// --help.
//
// Every failure past the question is silent.
func (c *Checker) Notify(out io.Writer, in io.Reader, mayAsk bool) {
	// A version that cannot be compared makes the whole feature inert: no notice
	// could ever come out of it. Bail before touching the settings or the network,
	// rather than asking for consent to a check that can only discard its own
	// answer. This is the dev-build case - an unstamped build, or the `git
	// describe` string a working-tree build carries ("1.2.3-4-gabc-dirty"), which
	// is by definition already at or past the release it names.
	if parseVersion(c.Version) == nil {
		return
	}

	s, err := c.load()
	if err != nil {
		return // unreadable settings: never nag, never check
	}

	if s.Updates == stateUnset {
		if !mayAsk {
			return
		}
		s.Updates = stateOff
		if c.prompt(in, out) {
			s.Updates = stateOn
		}
		// Persist the answer before acting on it: if the request below fails, or
		// the process is killed, the user must still not be asked a second time.
		// A dismissed prompt lands here as stateOff - silence is read as "no".
		if err := c.save(s); err != nil {
			return
		}
	}
	if s.Updates != stateOn {
		return
	}

	// GitHub is contacted at most once an interval. A first "on" leaves
	// LastChecked zero, so opting in checks straight away.
	age := c.now().Sub(s.LastChecked)
	if age < 0 || age >= c.interval() { // age < 0 => clock skew; treat as stale
		if latest, err := c.latest(); err == nil {
			s.LastChecked, s.LatestSeen = c.now(), latest
			_ = c.save(s)
		}
		// A failed request deliberately leaves LastChecked alone, so a machine
		// that happened to be offline retries next run instead of waiting a week.
	}

	if s.LatestSeen == "" || !Newer(c.Version, s.LatestSeen) {
		return
	}

	// The notice is throttled on its own clock: an upgrade the user chose not
	// to take is worth raising again in a week. Told in the future means clock
	// skew, and is treated as due.
	if since := c.now().Sub(s.LastNotified); since >= 0 && since < c.interval() {
		return
	}

	verb, target := c.upgradeHint()
	u := Upgrade{Version: s.LatestSeen, Verb: verb, Target: target}

	// On the surface that could put the question, the notice belongs in a
	// dialog too: a file-manager run has nowhere to print to, and its output
	// may be discarded entirely. Printing is the fallback, and the only path a
	// terminal run takes.
	announced := mayAsk && c.announce(u)
	if !announced {
		_, _ = fmt.Fprintf(out, "\n%s\n", c.noticeText(u))
	}
	// Recorded however it was delivered, and only once it has been: a notice
	// that was never shown must not start the week's silence.
	s.LastNotified = c.now()
	_ = c.save(s)
}

// Upgrade is a newer release that is available, and how to get it.
type Upgrade struct {
	Version string // the newer release, as its tag reads
	Verb    string // "Run" or "Download", per the install channel
	Target  string // the command to run, or the page to download from
}

// CheckNow is Notify's counterpart for the moment the host program hits
// something a newer release might handle better, rather than the routine
// end-of-run notice. It returns the available upgrade, or nil.
//
// Three deliberate differences from Notify:
//
//   - It never asks (only executes if the user already opted in).
//   - It ignores the throttle..
//   - It returns rather than prints ("an update is available and may
//     recognise that file").
func (c *Checker) CheckNow() *Upgrade {
	if parseVersion(c.Version) == nil {
		return nil // dev build: no comparison to make
	}
	s, err := c.load()
	if err != nil || s.Updates != stateOn {
		return nil
	}
	if latest, err := c.latest(); err == nil {
		s.LastChecked, s.LatestSeen = c.now(), latest
		_ = c.save(s)
	}
	if s.LatestSeen == "" || !Newer(c.Version, s.LatestSeen) {
		return nil
	}
	verb, target := c.upgradeHint()
	return &Upgrade{Version: s.LatestSeen, Verb: verb, Target: target}
}
