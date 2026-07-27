// The Windows first-run prompt.
//
// A run launched from Explorer's context menu gets a console window that is
// already waiting on a keypress before it closes, so the question costs the
// user nothing they were not about to do anyway and is asked right there.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package updatechecker

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func (*Checker) ask(question string, in io.Reader, out io.Writer) bool {
	_, _ = fmt.Fprintf(out, "\n%s [y/N]: ", question)
	// A read error leaves the line empty, which falls through to no — a console
	// that cannot be read from is not one that consented.
	switch strings.ToLower(strings.TrimSpace(readLine(in))) {
	case "y", "yes":
		return true
	}
	return false
}

// readLine reads one line, reusing in's own buffering when it has some rather
// than layering a second bufio.Reader over it. That matters when the host has
// more than one prompt on the same run — a fresh reader buffers past its
// newline and swallows input the next prompt is waiting for.
func readLine(in io.Reader) string {
	br, ok := in.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(in)
	}
	line, _ := br.ReadString('\n')
	return line
}
