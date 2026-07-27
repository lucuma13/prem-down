// The "auto-update" subcommand: the way to set the check from a terminal, for
// users who never see the first-run question because they do not use the
// file-manager integration, and for anyone changing their mind later.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package updatechecker

import (
	"fmt"
	"io"
	"time"
)

func (c *Checker) commandName() string {
	if c.CommandName != "" {
		return c.CommandName
	}
	return DefaultCommandName
}

func (c *Checker) usage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `Usage: %s %s [on|off|status]

Check GitHub for newer %s releases and mention them after a successful run.
This is the only network request %s ever makes.

Actions:
  on          check for new versions (at most once a week)
  off         never check
  status      show the current setting (the default when no action is given)

Options:
  -h, --help  show this help
`, c.Product, c.commandName(), c.Product, c.Product)
}

// Command executes the auto-update subcommand and returns the process exit code
// for the caller to return, writing through the injected streams rather than
// os.Stdout/os.Stderr.
func (c *Checker) Command(out, errw io.Writer, args []string) int {
	fatal := func(format string, a ...any) int {
		_, _ = fmt.Fprintf(errw, format+"\n", a...)
		return 1
	}

	action := "status" // a bare invocation reports, it never changes anything
	for _, a := range args {
		switch a {
		case "-h", "--help":
			c.usage(out)
			return 0
		case "on", "off", "status":
			action = a
		default:
			c.usage(errw)
			return fatal("error: unknown action %s", a)
		}
	}

	s, err := c.load()
	if err != nil {
		return fatal("error: %v", err)
	}

	switch action {
	case "on":
		s.AutoUpdate = stateOn
	case "off":
		s.AutoUpdate = stateOff
		s.LatestSeen = "" // drop any pending notice along with the setting
	}
	if action != "status" {
		if err := c.save(s); err != nil {
			return fatal("error: %v", err)
		}
	}

	path, err := c.configPath()
	if err != nil {
		return fatal("error: %v", err)
	}
	switch s.AutoUpdate {
	case stateOn:
		_, _ = fmt.Fprintln(out, "auto-update: on")
	case stateOff:
		_, _ = fmt.Fprintln(out, "auto-update: off")
	default:
		_, _ = fmt.Fprintln(out, "auto-update: not set (nothing is checked until you are asked, or set it here)")
	}
	_, _ = fmt.Fprintf(out, "settings file: %s\n", path)
	if s.AutoUpdate == stateOn && !s.LastChecked.IsZero() {
		_, _ = fmt.Fprintf(out, "last checked: %s (latest release: %s)\n",
			s.LastChecked.Local().Format(time.RFC1123), s.LatestSeen)
	}
	return 0
}
