package cron

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procCreateWaitableTimer = kernel32.NewProc("CreateWaitableTimerW")
	procSetWaitableTimer    = kernel32.NewProc("SetWaitableTimer")
	procCancelWaitableTimer = kernel32.NewProc("CancelWaitableTimer")
	procPowerCreateRequest  = kernel32.NewProc("PowerCreateRequest")
	procPowerSetRequest     = kernel32.NewProc("PowerSetRequest")
	procPowerClearRequest   = kernel32.NewProc("PowerClearRequest")
)

// waker arms one timer that resumes the machine from sleep.
type waker interface {
	arm(at time.Time) error
	disarm() error
	close()
}

// wakeTimer is a waitable timer whose due time resumes the system.
type wakeTimer struct{ handle windows.Handle }

func newWakeTimer() (*wakeTimer, error) {
	h, _, err := procCreateWaitableTimer.Call(0, 0, 0)
	if h == 0 {
		return nil, fmt.Errorf("CreateWaitableTimer: %w", err)
	}
	return &wakeTimer{handle: windows.Handle(h)}, nil
}

// filetimeEpochOffset is the seconds from 1601-01-01, the FILETIME epoch, to
// the Unix epoch.
const filetimeEpochOffset = 11644473600

func (w *wakeTimer) arm(at time.Time) error {
	// A positive due time is absolute, in 100ns units of UTC.
	due := (at.Unix()+filetimeEpochOffset)*10_000_000 + int64(at.Nanosecond()/100)
	const resume = 1
	if r, _, err := procSetWaitableTimer.Call(uintptr(w.handle), uintptr(unsafe.Pointer(&due)), 0, 0, 0, resume); r == 0 {
		return fmt.Errorf("SetWaitableTimer: %w", err)
	}
	return nil
}

func (w *wakeTimer) disarm() error {
	if r, _, err := procCancelWaitableTimer.Call(uintptr(w.handle)); r == 0 {
		return fmt.Errorf("CancelWaitableTimer: %w", err)
	}
	return nil
}

func (w *wakeTimer) close() { _ = windows.CloseHandle(w.handle) }

// reasonContext is the simple-string form of REASON_CONTEXT, which is all
// PowerCreateRequest reads when flags is POWER_REQUEST_CONTEXT_SIMPLE_STRING.
type reasonContext struct {
	version uint32
	flags   uint32
	reason  *uint16
}

const (
	powerRequestContextSimpleString = 0x1
	powerRequestSystemRequired      = 1
)

// keepAwake stops the system from going back to sleep until release is
// called. After a wake timer resumes it, Windows sleeps again on its short
// unattended idle timeout, which would suspend the job mid-run.
func keepAwake() (release func() error, err error) {
	reason, err := windows.UTF16PtrFromString("wincron is running a wake=yes job")
	if err != nil {
		return nil, err
	}
	ctx := reasonContext{flags: powerRequestContextSimpleString, reason: reason}
	r, _, callErr := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&ctx)))
	runtime.KeepAlive(reason)
	h := windows.Handle(r)
	if h == windows.InvalidHandle {
		return nil, fmt.Errorf("PowerCreateRequest: %w", callErr)
	}
	if r, _, callErr := procPowerSetRequest.Call(uintptr(h), powerRequestSystemRequired); r == 0 {
		return nil, errors.Join(fmt.Errorf("PowerSetRequest: %w", callErr), windows.CloseHandle(h))
	}
	return func() error {
		var clearErr error
		if r, _, callErr := procPowerClearRequest.Call(uintptr(h), powerRequestSystemRequired); r == 0 {
			clearErr = fmt.Errorf("PowerClearRequest: %w", callErr)
		}
		return errors.Join(clearErr, windows.CloseHandle(h))
	}, nil
}
