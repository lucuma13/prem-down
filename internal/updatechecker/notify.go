// The runtime half of the check: the first-run question, the throttle, and the
// notice itself.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package updatechecker

import (
	"fmt"
	"io"
	"time"
)

// question is the first-run prompt. It names what the check actually does,
// because the thing being consented to is the one network request the host
// program makes.
func (c *Checker) question() string {
	if c.Question != "" {
		return c.Question
	}
	return fmt.Sprintf("Check for new %s versions automatically?\n"+
		"This contacts GitHub about once a week to compare version numbers. "+
		"Your files are never uploaded.", c.Product)
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

// Notify is called once per run, after the host program's real work has
// succeeded, and reports a newer release on out.
//
// mayAsk says this run is attached to a surface where a question can be put to
// the user and answered — for a file-manager integration, the console window or
// desktop dialog it already owns. The first-run question is only asked there:
// those are the surfaces where the user has no other way to discover the
// setting, and the ones with somewhere to put a question. A plain terminal run
// stays silent and scriptable; that user has the auto-update subcommand and
// --help.
//
// Every failure past the question is silent.
func (c *Checker) Notify(out io.Writer, in io.Reader, mayAsk bool) {
	// A version that cannot be compared makes the whole feature inert: no notice
	// could ever come out of it. Bail before touching the settings or the network,
	// rather than asking for consent to a check that can only discard its own
	// answer. This is the dev-build case — an unstamped build, or the `git
	// describe` string a working-tree build carries ("1.2.3-4-gabc-dirty"), which
	// is by definition already at or past the release it names.
	if parseVersion(c.Version) == nil {
		return
	}

	s, err := c.load()
	if err != nil {
		return // unreadable settings: never nag, never check
	}

	if s.AutoUpdate == stateUnset {
		if !mayAsk {
			return
		}
		s.AutoUpdate = stateOff
		if c.prompt(in, out) {
			s.AutoUpdate = stateOn
		}
		// Persist the answer before acting on it: if the request below fails, or
		// the process is killed, the user must still not be asked a second time.
		// A dismissed prompt lands here as stateOff — silence is read as "no".
		if err := c.save(s); err != nil {
			return
		}
	}
	if s.AutoUpdate != stateOn {
		return
	}

	// The request is throttled, the notice is not: LatestSeen persists, so a
	// pending upgrade is reported on every run until it is taken, while GitHub
	// is contacted at most once an interval. A first "on" leaves LastChecked
	// zero, so opting in checks straight away.
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
	verb, target := c.upgradeHint()
	_, _ = fmt.Fprintf(out, "\n%s %s is available. %s: %s\n", c.Product, s.LatestSeen, verb, target)
}
