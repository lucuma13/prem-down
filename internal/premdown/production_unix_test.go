//go:build unix

// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package premdown

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A Production holds projects, media and settings — never a pipe, socket or
// device. One found in the tree has nothing meaningful to copy, so it is
// skipped with a warning rather than copied (which would block on the pipe) or
// counted as a failure (nothing about the user's Production is broken).
//
// Unix-only because there is no portable way to create a non-regular file.
func TestDowngradeProductionSkipsNonRegularFiles(t *testing.T) {
	src := newProduction(t, "Pipes")
	if err := syscall.Mkfifo(filepath.Join(src, "pipe"), 0o600); err != nil {
		t.Skipf("cannot create a FIFO: %v", err)
	}
	dst := src + "_downgraded"

	var errBuf bytes.Buffer
	d := &Downgrader{Err: &errBuf}
	if err := d.DowngradeProduction(src, dst, 43, false); err != nil {
		t.Fatalf("a non-regular file must not fail the mirror: %v", err)
	}
	if !strings.Contains(errBuf.String(), "pipe (not a regular file)") {
		t.Errorf("the skip should be reported:\n%s", errBuf.String())
	}
	if _, err := os.Lstat(filepath.Join(dst, "pipe")); err == nil {
		t.Error("the pipe should not have been reproduced in the copy")
	}
	// The rest of the Production is mirrored as usual.
	if _, err := os.Stat(filepath.Join(dst, "Untitled"+PrprojExt)); err != nil {
		t.Errorf("the rest of the Production should still be mirrored: %v", err)
	}
}
