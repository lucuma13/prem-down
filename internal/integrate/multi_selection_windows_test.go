// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package integrate

import (
	"bytes"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// hasEmbeddingArg gates whether prem-down becomes the COM server, so it must
// recognise exactly COM's activation flag and nothing a normal user would type.
func TestHasEmbeddingArg(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"-Embedding"}, true},
		{[]string{"/Embedding"}, true},
		{[]string{"-embedding"}, true},
		{[]string{"a.prproj", "-Embedding"}, true},
		{nil, false},
		{[]string{"a.prproj", "b.prproj"}, false},
		{[]string{"--gui", "a.prproj"}, false},
		{[]string{"integrate"}, false},
		// A bare "Embedding" is a plausible filename, not COM's switch.
		{[]string{"Embedding"}, false},
	} {
		if got := hasEmbeddingArg(tc.args); got != tc.want {
			t.Errorf("hasEmbeddingArg(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// TestDropTargetServerHelper is not a real test: it is the child half of
// TestDropTargetServerSurvivesRegistration. When re-invoked with
// PREM_DOWN_COM_HELPER=1 it enters the COM server exactly as an Explorer
// "-Embedding" activation would, in its own process so a startup crash there
// (e.g. a Win32 proc looked up in the wrong DLL, which panics at call time)
// cannot take the test run down with it.
func TestDropTargetServerHelper(t *testing.T) {
	if os.Getenv("PREM_DOWN_COM_HELPER") != "1" {
		t.Skip("helper process for TestDropTargetServerSurvivesRegistration")
	}
	// MaybeRunCOMServer takes a downgrader, but no Drop arrives here: nothing in
	// this test activates the drop target, so the server registers and then sits
	// in its pump until the parent kills it (or the 60s safety timeout fires).
	//
	// Converting nothing is also the right answer if a Drop somehow did arrive.
	// Registration uses the production CLSID, so for the few seconds this helper
	// runs it is a live handler for the real context-menu verb, and a right-click
	// on the machine running the tests could be routed here.
	stub := func([]string) (string, bool) { return "", false }
	if !MaybeRunCOMServer([]string{"-Embedding"}, stub) {
		t.Fatal("MaybeRunCOMServer did not enter server mode for -Embedding")
	}
}

// Smoke-test the "-Embedding" activation path: the server must survive
// CoInitializeEx, the thread-id lookup and CoRegisterClassObject, and sit in
// its message pump waiting for Explorer's Drop. Registration takes
// milliseconds and every startup failure exits the process, so "still running
// after a few seconds" is the pass signal; the child is then killed rather
// than waiting out the server's own 60s safety timeout.
func TestDropTargetServerSurvivesRegistration(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestDropTargetServerHelper$", "-test.v") //nolint:gosec // G204: exe is os.Executable(), this test binary re-invoking itself, not external input
	cmd.Env = append(os.Environ(), "PREM_DOWN_COM_HELPER=1")
	// No console window for the child: runDropTargetServer hides its console,
	// and without this it would inherit — and hide — the developer's terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("COM server exited during startup/registration (err=%v):\n%s", err, out.String())
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

// runAndReport is the whole Drop-side sequence: convert the selection, report
// it, and release the server. Every part of it is exercised here except the
// MessageBoxW call itself — the presenter is swapped out, because a modal box
// on a CI agent is one nobody will ever dismiss.
//
// The sequence matters as much as the pieces: runDropTargetServer waits on
// workDone before letting the process exit, so a path that reports without
// closing it would hang every context-menu conversion.
func TestRunAndReport(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary string
		failed  bool
	}{
		{"success", "wrote a_downgraded.prproj", false},
		{"failure", "error: a.prproj: not a Premiere project", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreServerState(t)

			var gotFiles []string
			downgrade = func(files []string) (string, bool) {
				gotFiles = files
				return tc.summary, tc.failed
			}
			var gotSummary string
			var gotFailed bool
			shown := 0
			showResult = func(summary string, failed bool) {
				gotSummary, gotFailed = summary, failed
				shown++
			}

			want := []string{`C:\Clips\a.prproj`, `C:\Clips\b.prproj`}
			runAndReport(want)

			if len(gotFiles) != len(want) || gotFiles[0] != want[0] || gotFiles[1] != want[1] {
				t.Errorf("downgrader got %v, want %v", gotFiles, want)
			}
			// Exactly one report for the whole selection, however many files it
			// held — that is the point of receiving the drop as one call rather
			// than letting Explorer invoke a command verb per file.
			if shown != 1 {
				t.Fatalf("the outcome was reported %d times, want exactly 1", shown)
			}
			if gotSummary != tc.summary || gotFailed != tc.failed {
				t.Errorf("reported %q/%v, want %q/%v", gotSummary, gotFailed, tc.summary, tc.failed)
			}
			select {
			case <-workDone:
			default:
				t.Error("workDone was not closed; the server would wait for it forever")
			}
		})
	}
}

// A Drop with nothing usable in it must still end the pump rather than leave an
// activated server sitting in memory.
func TestDropWithNoDowngraderEndsTheServer(t *testing.T) {
	restoreServerState(t)
	downgrade = nil
	// A nil data object yields no files, which is the same branch Explorer takes
	// us down when the selection carries no CF_HDROP.
	var effect uint32
	if hr := dropDrop(nil, nil, 0, 0, &effect); hr != sOK {
		t.Errorf("Drop should always succeed, got hr=%#x", hr)
	}
	if workStarted {
		t.Error("no work should have been started")
	}
}

// restoreServerState isolates a test from the package-level server state and
// puts it back afterwards, so tests cannot leak into one another.
func restoreServerState(t *testing.T) {
	t.Helper()
	oldDowngrade, oldShow, oldDone, oldStarted, oldThread := downgrade, showResult, workDone, workStarted, serverThreadID
	// Thread 0 is never a real thread, so the WM_QUIT post fails harmlessly
	// instead of disturbing whatever thread happens to be running the tests.
	serverThreadID = 0
	workDone = make(chan struct{})
	workStarted = false
	t.Cleanup(func() {
		downgrade, showResult, workDone, workStarted, serverThreadID = oldDowngrade, oldShow, oldDone, oldStarted, oldThread
	})
}
