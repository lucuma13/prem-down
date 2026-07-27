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
	if !MaybeRunCOMServer([]string{"-Embedding"}) {
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

// makeCmdLine must quote each argument so paths with spaces survive as one
// argument through CreateProcess -> CommandLineToArgvW.
func TestMakeCmdLine(t *testing.T) {
	got := makeCmdLine([]string{`C:\Program Files\prem-down\prem-down.exe`, "--gui", `C:\My Clips\a b.prproj`})
	want := `"C:\Program Files\prem-down\prem-down.exe" --gui "C:\My Clips\a b.prproj"`
	if got != want {
		t.Errorf("makeCmdLine =\n  %q\nwant\n  %q", got, want)
	}
}
