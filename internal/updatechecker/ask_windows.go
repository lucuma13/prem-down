// The Windows first-run prompt.
//
// A run launched from Explorer's context menu has no console — the shell
// activates the handler as a background COM server — so the question is put in
// a message box, the same way the result is reported.
//
// user32 is on Windows' KnownDLLs list, so it always resolves from System32 and
// NewLazyDLL cannot be DLL-planted here.
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package updatechecker

import (
	"io"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	moduser32       = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = moduser32.NewProc("MessageBoxW")
)

const (
	mbYesNo          = 0x00000004
	mbIconQuestion   = 0x00000020
	mbDefButton2     = 0x00000100 // "No" is the default: consent must be deliberate
	mbSetForeground  = 0x00010000
	mbTopmost        = 0x00040000
	idYes            = 6
	promptBoxOptions = mbYesNo | mbIconQuestion | mbDefButton2 | mbSetForeground | mbTopmost
)

func (c *Checker) ask(question string, _ io.Reader, _ io.Writer) bool {
	text, err := syscall.UTF16PtrFromString(question)
	if err != nil {
		return false
	}
	caption, err := syscall.UTF16PtrFromString(c.Product)
	if err != nil {
		return false
	}
	// MessageBoxW runs a modal message loop on the calling thread, so pin the
	// goroutine to one OS thread while it is up.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	//nolint:gosec // G103: the uintptr conversions are the Win32 calling convention; the buffers are kept alive below.
	r, _, _ := procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(caption)), promptBoxOptions)
	// Those conversions are invisible to the GC; hold the buffers until the call
	// has returned.
	runtime.KeepAlive(text)
	runtime.KeepAlive(caption)
	return r == idYes
}
