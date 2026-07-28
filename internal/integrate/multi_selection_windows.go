// Receives an entire multi-file File Explorer selection in one activation.
//
// This file exists only to support selecting several projects at once. A Drop
// Target verb packages a selection of several files into a single Drop call.
//
// The cost of that is a COM handler: the verb's key points at a CLSID, that
// CLSID is registered as a LocalServer32 on prem-down's own exe (see
// integrate_windows.go for the registration), and COM relaunches this exe with
// "-Embedding" to activate it.
//
// Once activated, the code here registers a class factory, waits for Explorer's
// Drop, pulls the selected paths out of the CF_HDROP, downgrades them in this
// same process, and reports the outcome in a message box.
//
// The COM plumbing is hand-rolled on the syscall package (vtables built with
// syscall.NewCallback) so prem-down stays a single dependency-free binary. Only
// the two interfaces Explorer needs are implemented: IClassFactory (to vend the
// handler) and IDropTarget (to receive the selection).
//
// Copyright (c) 2026 Luis Gómez Gutiérrez. License: MIT.

package integrate

import (
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// ole32, shell32 and user32 are all on Windows' KnownDLLs list, so they are
// always resolved from System32 and NewLazyDLL cannot be DLL-planted here.
var (
	modole32    = syscall.NewLazyDLL("ole32.dll")
	modshell32  = syscall.NewLazyDLL("shell32.dll")
	moduser32   = syscall.NewLazyDLL("user32.dll")
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")

	procCoInitializeEx        = modole32.NewProc("CoInitializeEx")
	procCoUninitialize        = modole32.NewProc("CoUninitialize")
	procCoRegisterClassObject = modole32.NewProc("CoRegisterClassObject")
	procCoRevokeClassObject   = modole32.NewProc("CoRevokeClassObject")
	procCLSIDFromString       = modole32.NewProc("CLSIDFromString")
	procReleaseStgMedium      = modole32.NewProc("ReleaseStgMedium")

	procDragQueryFileW = modshell32.NewProc("DragQueryFileW")

	procGetMessageW        = moduser32.NewProc("GetMessageW")
	procTranslateMessage   = moduser32.NewProc("TranslateMessage")
	procDispatchMessageW   = moduser32.NewProc("DispatchMessageW")
	procPostThreadMessageW = moduser32.NewProc("PostThreadMessageW")
	procShowWindow         = moduser32.NewProc("ShowWindow")
	procMessageBoxW        = moduser32.NewProc("MessageBoxW")

	// GetCurrentThreadId lives in kernel32 (not user32, where the other
	// thread-message procs come from); LazyProc panics at call time if the
	// name is looked up in the wrong DLL.
	procGetCurrentThreadID = modkernel32.NewProc("GetCurrentThreadId")
	procGetConsoleWindow   = modkernel32.NewProc("GetConsoleWindow")
)

// ptr converts a Go pointer to the uintptr a syscall argument list wants. It is
// the single audited use of unsafe.Pointer for argument passing: routing every
// call through it keeps the rest of the file free of raw conversions (so go
// vet's unsafeptr and gosec's G103 have one place to look), and taking a
// pointer's address through it escapes the pointee to the heap, where Go's
// non-moving GC leaves it put for the duration of the call.
//
//nolint:gosec // G103: Win32/COM interop is inherently unsafe; audited here.
func ptr[T any](p *T) uintptr { return uintptr(unsafe.Pointer(p)) }

// call invokes a Win32/COM entry point and returns its primary result. These
// APIs report failure through that return value (checked at the call site where
// it matters) or, for the void UI calls, not at all; LazyProc.Call's error is
// the always-non-nil GetLastError shim, so it is intentionally discarded.
func call(p *syscall.LazyProc, a ...uintptr) uintptr {
	r, _, _ := p.Call(a...)
	return r
}

// failed reports whether an HRESULT indicates failure (its sign bit is set),
// without narrowing the uintptr.
func failed(hr uintptr) bool { return hr&0x80000000 != 0 }

const (
	coinitApartmentThreaded = 0x2
	clsctxLocalServer       = 0x4
	regclsSingleUse         = 0 // REGCLS_SINGLEUSE

	sOK               = 0
	eNoInterface      = uintptr(0x80004002)
	classENoAggregate = uintptr(0x80040110) // CLASS_E_NOAGGREGATION

	cfHDROP         = 15
	dvaspectContent = 1
	tymedHGlobal    = 1
	dropeffectCopy  = 1

	wmQuit = 0x0012
	swHide = 0 // ShowWindow: hide the window

	// MessageBoxW flags. SETFOREGROUND and TOPMOST matter here because the
	// server owns no window and was launched by COM rather than by the user
	// clicking something: without them the box can open behind Explorer.
	mbOK              = 0x00000000
	mbIconError       = 0x00000010
	mbIconInformation = 0x00000040
	mbSetForeground   = 0x00010000
	mbTopmost         = 0x00040000

	// serverTimeout bounds how long an activated server lingers if Explorer
	// never drives it to a Drop (e.g. the activation is abandoned), so it can
	// never hang around as an orphaned background process. It is stopped as
	// soon as a Drop arrives: past that point the conversion sets the pace, and
	// a large Production can legitimately outlast this.
	serverTimeout = 60 * time.Second
)

// serverThreadID is the STA thread running the message pump; Drop (and the
// safety timer) post WM_QUIT to it to end the pump. Kept as uintptr (a thread
// id fits) so it feeds PostThreadMessageW without a narrowing conversion.
var serverThreadID uintptr

// serverTimer is the safety timeout above, kept here so Drop can stop it.
var serverTimer *time.Timer

// downgrade does the actual conversion; supplied by the caller of
// MaybeRunCOMServer so this package keeps to the Windows plumbing and the
// command layer keeps the job planning. Read only from the Drop callback.
var downgrade Downgrader

// workStarted says Drop handed a selection to the worker goroutine, so the
// server must wait for it before letting the process exit. Both the write (in
// Drop) and the read (after the pump) happen on the STA thread.
var workStarted bool

// workDone is closed by the worker once the conversion and its message box are
// finished.
var workDone = make(chan struct{})

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// The two interface ids Explorer asks our objects for.
var (
	iidIUnknown      = guid{0x00000000, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIClassFactory = guid{0x00000001, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIDropTarget   = guid{0x00000122, 0, 0, [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

func guidEqual(a, b *guid) bool {
	return a.Data1 == b.Data1 && a.Data2 == b.Data2 && a.Data3 == b.Data3 && a.Data4 == b.Data4
}

type iUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

type iClassFactoryVtbl struct {
	iUnknownVtbl
	CreateInstance uintptr
	LockServer     uintptr
}

type iDropTargetVtbl struct {
	iUnknownVtbl
	DragEnter uintptr
	DragOver  uintptr
	DragLeave uintptr
	Drop      uintptr
}

// iDataObjectVtbl mirrors just enough of the IDataObject vtable to call GetData
// (the fourth entry). Walking Explorer's object through a typed struct avoids a
// uintptr->unsafe.Pointer vtable computation; the trailing methods are unused.
type iDataObjectVtbl struct {
	iUnknownVtbl
	GetData uintptr
}

// A COM object is just a struct whose first field is a pointer to its vtable;
// the pointer to the struct is the interface pointer Explorer holds. A single
// static instance of each suffices (this is a single-use, single-thread server).
type (
	classFactory struct{ vtbl *iClassFactoryVtbl }
	dropTarget   struct{ vtbl *iDropTargetVtbl }
)

var (
	factoryVtbl        iClassFactoryVtbl
	dropVtbl           iDropTargetVtbl
	factoryInstance    classFactory
	dropTargetInstance dropTarget
)

// formatEtc / stgMedium mirror the Win32 structs; Go inserts the same alignment
// padding the C layouts have on amd64 (the DVTARGETDEVICE* and hGlobal land on
// 8-byte offsets). Fields we neither set nor read — the target-device pointer
// and the fields the shell fills in and ReleaseStgMedium consumes — are blank
// so they hold their place in the layout without tripping the unused-field
// check.
type formatEtc struct {
	cfFormat uint16
	_        uintptr // DVTARGETDEVICE* target-device pointer, left NULL
	dwAspect uint32
	lindex   int32
	tymed    uint32
}

type stgMedium struct {
	_       uint32  // tymed (set by the shell, unread)
	_       uint32  // padding, aligns hGlobal to 8
	hGlobal uintptr // the HDROP we read
	_       uintptr // pUnkForRelease (consumed by ReleaseStgMedium)
}

// setupCOMObjects wires the vtables to Go callbacks. Refcounting is deliberately
// trivial — AddRef/Release return a constant nonzero so the static objects are
// never considered freed; the process controls its own lifetime and exits right
// after the Drop.
func setupCOMObjects() {
	factoryVtbl = iClassFactoryVtbl{
		iUnknownVtbl: iUnknownVtbl{
			QueryInterface: syscall.NewCallback(factoryQueryInterface),
			AddRef:         syscall.NewCallback(comAddRefRelease),
			Release:        syscall.NewCallback(comAddRefRelease),
		},
		CreateInstance: syscall.NewCallback(factoryCreateInstance),
		LockServer:     syscall.NewCallback(comLockServer),
	}
	factoryInstance.vtbl = &factoryVtbl

	dropVtbl = iDropTargetVtbl{
		iUnknownVtbl: iUnknownVtbl{
			QueryInterface: syscall.NewCallback(dropQueryInterface),
			AddRef:         syscall.NewCallback(comAddRefRelease),
			Release:        syscall.NewCallback(comAddRefRelease),
		},
		DragEnter: syscall.NewCallback(dropDragEnter),
		DragOver:  syscall.NewCallback(dropDragOver),
		DragLeave: syscall.NewCallback(dropDragLeave),
		Drop:      syscall.NewCallback(dropDrop),
	}
	dropTargetInstance.vtbl = &dropVtbl
}

// The COM methods take their pointer arguments as typed pointers rather than
// uintptr so their bodies never convert a uintptr back into a pointer — that
// keeps them clean under go vet's unsafeptr check. Scalar-by-value slots (a
// DWORD grfKeyState, a POINTL pt — 8 bytes, one register slot on amd64) that we
// ignore stay as uintptr.
func comAddRefRelease(_ unsafe.Pointer) uintptr { return 1 }

func comLockServer(_ unsafe.Pointer, _ uintptr) uintptr { return sOK }

func factoryQueryInterface(this unsafe.Pointer, riid *guid, ppv *unsafe.Pointer) uintptr {
	if guidEqual(riid, &iidIUnknown) || guidEqual(riid, &iidIClassFactory) {
		*ppv = this
		return sOK
	}
	*ppv = nil
	return eNoInterface
}

func factoryCreateInstance(_ unsafe.Pointer, pUnkOuter unsafe.Pointer, riid *guid, ppv *unsafe.Pointer) uintptr {
	if pUnkOuter != nil { // aggregation is not supported
		*ppv = nil
		return classENoAggregate
	}
	if guidEqual(riid, &iidIUnknown) || guidEqual(riid, &iidIDropTarget) {
		*ppv = unsafe.Pointer(&dropTargetInstance) //nolint:gosec // G103: hand back our static handler as the new instance.
		return sOK
	}
	*ppv = nil
	return eNoInterface
}

func dropQueryInterface(this unsafe.Pointer, riid *guid, ppv *unsafe.Pointer) uintptr {
	if guidEqual(riid, &iidIUnknown) || guidEqual(riid, &iidIDropTarget) {
		*ppv = this
		return sOK
	}
	*ppv = nil
	return eNoInterface
}

// DragEnter/DragOver report that a copy would be accepted so Explorer proceeds
// to Drop. DragLeave is a no-op.
func dropDragEnter(_ unsafe.Pointer, _ unsafe.Pointer, _ uintptr, _ uintptr, pdwEffect *uint32) uintptr {
	*pdwEffect = dropeffectCopy
	return sOK
}

func dropDragOver(_ unsafe.Pointer, _ uintptr, _ uintptr, pdwEffect *uint32) uintptr {
	*pdwEffect = dropeffectCopy
	return sOK
}

func dropDragLeave(_ unsafe.Pointer) uintptr { return sOK }

// dropDrop receives the selection. It reads the paths and returns to Explorer
// straight away, converting on another goroutine: Drop is a blocking
// out-of-process COM call made from Explorer's own thread, so doing the work
// inline would freeze the window the user just clicked in for as long as the
// conversion takes — and could trip COM's "server is busy" dialog.
func dropDrop(_ unsafe.Pointer, pDataObj unsafe.Pointer, _ uintptr, _ uintptr, pdwEffect *uint32) uintptr {
	*pdwEffect = dropeffectCopy
	files := extractHDROPFiles(pDataObj)
	if len(files) == 0 || downgrade == nil {
		// Nothing to hand off; end the pump. Runs on the STA thread, so posting
		// to it is safe.
		call(procPostThreadMessageW, serverThreadID, wmQuit, 0, 0)
		return sOK
	}
	if serverTimer != nil {
		serverTimer.Stop() // the conversion sets the pace
	}
	workStarted = true
	go runAndReport(files)
	return sOK
}

// runAndReport does the conversion off the STA thread, shows the outcome, and
// only then ends the pump.
func runAndReport(files []string) {
	// MessageBoxW runs a modal message loop on whatever thread calls it, so pin
	// this goroutine to one OS thread for the duration.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(workDone)
	defer call(procPostThreadMessageW, serverThreadID, wmQuit, 0, 0)

	summary, bad := downgrade(files)
	showResult(summary, bad)
}

// showResult puts the outcome in front of the user. It is a variable so the
// test for the sequence above can capture what would be shown: a real modal box
// on a CI agent is one nobody will ever dismiss, and the server would hang
// waiting for it.
var showResult = func(summary string, failed bool) {
	icon := uintptr(mbIconInformation)
	if failed {
		icon = mbIconError
	}
	messageBox(summary, contextMenuTitle, mbOK|icon|mbSetForeground|mbTopmost)
}

// messageBox shows a modal box and returns which button was pressed. A string
// that cannot be converted (only possible if it contains a NUL) is dropped
// rather than reported: this is the path that reports errors, so it has no
// better channel of its own to fail into.
func messageBox(text, caption string, flags uintptr) uintptr {
	textPtr, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return 0
	}
	captionPtr, err := syscall.UTF16PtrFromString(caption)
	if err != nil {
		return 0
	}
	r := call(procMessageBoxW, 0, ptr(textPtr), ptr(captionPtr), flags)
	// The uintptr conversions above are invisible to the GC, so hold the
	// buffers until the call has returned.
	runtime.KeepAlive(textPtr)
	runtime.KeepAlive(captionPtr)
	return r
}

// extractHDROPFiles pulls the selected paths out of the data object Explorer
// passed to Drop. It asks for CF_HDROP as an HGLOBAL, enumerates the paths with
// DragQueryFileW, then releases the medium.
func extractHDROPFiles(pDataObj unsafe.Pointer) []string {
	if pDataObj == nil {
		// Explorer should never do this, but reading the vtable off a null
		// interface pointer would take the whole server down with it, and this
		// code runs inside a COM callback where a panic has nowhere to go.
		return nil
	}
	fe := formatEtc{cfFormat: cfHDROP, dwAspect: dvaspectContent, lindex: -1, tymed: tymedHGlobal}
	var med stgMedium
	dataObj := (*struct{ vtbl *iDataObjectVtbl })(pDataObj)
	hr, _, _ := syscall.SyscallN(dataObj.vtbl.GetData, uintptr(pDataObj), ptr(&fe), ptr(&med))
	if failed(hr) {
		return nil
	}
	defer call(procReleaseStgMedium, ptr(&med))

	hDrop := med.hGlobal
	count := call(procDragQueryFileW, hDrop, 0xFFFFFFFF, 0, 0)
	files := make([]string, 0, int(count))
	for i := 0; i < int(count); i++ {
		// A first call with a nil buffer returns the length (excluding the NUL),
		// so paths longer than MAX_PATH are handled without a fixed buffer.
		length := call(procDragQueryFileW, hDrop, uintptr(i), 0, 0)
		if length == 0 {
			continue
		}
		buf := make([]uint16, int(length)+1)
		call(procDragQueryFileW, hDrop, uintptr(i), ptr(&buf[0]), uintptr(len(buf)))
		files = append(files, syscall.UTF16ToString(buf))
	}
	return files
}

// messagePump runs the STA message loop. In a single-threaded apartment, COM
// delivers incoming interface calls (DragEnter, Drop, ...) through the message
// queue, so this pump is what actually drives our handler. It returns when
// GetMessage yields WM_QUIT (0) or an error (-1).
func messagePump() {
	var msg struct {
		hwnd     uintptr
		message  uint32
		wParam   uintptr
		lParam   uintptr
		time     uint32
		pt       struct{ x, y int32 }
		lPrivate uint32
	}
	for {
		r := call(procGetMessageW, ptr(&msg), 0, 0, 0)
		if int32(r) <= 0 { //nolint:gosec // G115: GetMessageW returns a 32-bit BOOL; 0 is WM_QUIT and -1 is an error, both end the pump.
			return
		}
		call(procTranslateMessage, ptr(&msg))
		call(procDispatchMessageW, ptr(&msg))
	}
}

// runDropTargetServer runs prem-down as the COM LocalServer for the Drop Target
// class: it registers the class factory, pumps messages until the Drop hands
// off the selection (or the safety timeout fires), then tears down.
func runDropTargetServer() {
	// COM launches this LocalServer as a console-subsystem process, so Windows
	// hands it a console window it never writes to. Hide it up front so the only
	// window the user ever sees is the message box.
	if hwnd := call(procGetConsoleWindow); hwnd != 0 {
		call(procShowWindow, hwnd, swHide)
	}

	// COM apartment, the message pump, and the class object must all live on one
	// OS thread for the STA to work.
	runtime.LockOSThread()

	call(procCoInitializeEx, 0, coinitApartmentThreaded)
	defer call(procCoUninitialize)

	serverThreadID = call(procGetCurrentThreadID)

	setupCOMObjects()

	var clsid guid
	clsidStr, err := syscall.UTF16PtrFromString(dropHandlerCLSID)
	if err != nil {
		return
	}
	call(procCLSIDFromString, ptr(clsidStr), ptr(&clsid))

	var token uint32
	hr := call(procCoRegisterClassObject,
		ptr(&clsid), ptr(&factoryInstance), clsctxLocalServer, regclsSingleUse, ptr(&token))
	if failed(hr) {
		return
	}
	defer call(procCoRevokeClassObject, uintptr(token))

	serverTimer = time.AfterFunc(serverTimeout, func() {
		call(procPostThreadMessageW, serverThreadID, wmQuit, 0, 0)
	})
	defer serverTimer.Stop()

	messagePump()

	// The pump ends as soon as WM_QUIT lands, which for a Drop is before the
	// conversion has finished — and whoever posted it, returning here would exit
	// the process out from under the worker. Wait for it.
	if workStarted {
		<-workDone
	}
}

// MaybeRunCOMServer runs the Drop Target server and reports true when prem-down
// was activated by COM (launched with "-Embedding"); main() then returns
// without doing any normal CLI parsing.
//
// run is what converts the selection Explorer drops. It is injected because the
// planning it needs — grouping a mixed selection of projects and Productions —
// belongs to the command layer, not to this file's Win32 plumbing.
func MaybeRunCOMServer(args []string, run Downgrader) bool {
	if !hasEmbeddingArg(args) {
		return false
	}
	downgrade = run
	runDropTargetServer()
	return true
}

// hasEmbeddingArg reports whether COM launched us for activation: it appends
// "-Embedding" (older shells use "/Embedding") to the LocalServer32 command.
// The switch prefix is required — a bare "Embedding" is a plausible filename
// and must be treated as an ordinary positional argument.
func hasEmbeddingArg(args []string) bool {
	for _, a := range args {
		if (strings.HasPrefix(a, "-") || strings.HasPrefix(a, "/")) &&
			strings.EqualFold(strings.TrimLeft(a, "-/"), "Embedding") {
			return true
		}
	}
	return false
}
