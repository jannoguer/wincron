package cron

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procSpec describes the process to create for a job. Go's os/exec cannot set
// STARTUPINFO.lpDesktop, so processes are created with CreateProcess directly.
type procSpec struct {
	appName string
	cmdLine string
	// env is the complete environment; nil inherits the scheduler's.
	env []string
	dir string
	// desktop is lpDesktop, such as winsta0\default; empty inherits.
	desktop string
	// token runs the process as that user; zero uses the scheduler's own.
	token windows.Token
	// flags are extra creation flags.
	flags uint32
}

// processResult is how a process ended.
type processResult struct {
	exitCode uint32
	// terminated is set when ctx ended first and the process tree was killed.
	terminated bool
}

func closeHandles(handles ...windows.Handle) {
	for _, h := range handles {
		_ = windows.CloseHandle(h)
	}
}

func inheritable() *windows.SecurityAttributes {
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
}

// newOutputPipe returns a pipe whose write end the child inherits and whose
// read end stays with this process.
func newOutputPipe() (read, write windows.Handle, err error) {
	if err := windows.CreatePipe(&read, &write, inheritable(), 0); err != nil {
		return 0, 0, fmt.Errorf("creating output pipe: %w", err)
	}
	if err := windows.SetHandleInformation(read, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		closeHandles(read, write)
		return 0, 0, fmt.Errorf("creating output pipe: %w", err)
	}
	return read, write, nil
}

// openNull returns an inheritable handle on NUL for the child's stdin.
func openNull() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		inheritable(), windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return 0, fmt.Errorf("opening NUL: %w", err)
	}
	return h, nil
}

// createSuspended starts the process suspended, so it cannot spawn a child
// before it is in the job object. Only stdin and stdout (also used for
// stderr) are inherited, via PROC_THREAD_ATTRIBUTE_HANDLE_LIST, so handles of
// jobs running concurrently never leak into each other.
func createSuspended(spec procSpec, stdin, stdout windows.Handle) (process, thread windows.Handle, err error) {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, 0, err
	}
	defer attrs.Delete()
	inherited := []windows.Handle{stdin, stdout}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&inherited[0]), uintptr(len(inherited))*unsafe.Sizeof(inherited[0])); err != nil {
		return 0, 0, err
	}
	si := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb:        uint32(unsafe.Sizeof(windows.StartupInfoEx{})),
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  stdin,
			StdOutput: stdout,
			StdErr:    stdout,
		},
		ProcThreadAttributeList: attrs.List(),
	}
	if spec.desktop != "" {
		if si.Desktop, err = windows.UTF16PtrFromString(spec.desktop); err != nil {
			return 0, 0, err
		}
	}
	app, err := windows.UTF16PtrFromString(spec.appName)
	if err != nil {
		return 0, 0, err
	}
	cmdLine, err := windows.UTF16PtrFromString(spec.cmdLine)
	if err != nil {
		return 0, 0, err
	}
	var env *uint16
	if spec.env != nil {
		if env, err = envBlock(spec.env); err != nil {
			return 0, 0, err
		}
	}
	var dir *uint16
	if spec.dir != "" {
		if dir, err = windows.UTF16PtrFromString(spec.dir); err != nil {
			return 0, 0, err
		}
	}
	flags := spec.flags | windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT
	var pi windows.ProcessInformation
	if spec.token != 0 {
		err = windows.CreateProcessAsUser(spec.token, app, cmdLine, nil, nil, true, flags, env, dir, &si.StartupInfo, &pi)
	} else {
		err = windows.CreateProcess(app, cmdLine, nil, nil, true, flags, env, dir, &si.StartupInfo, &pi)
	}
	runtime.KeepAlive(inherited)
	if err != nil {
		return 0, 0, err
	}
	return pi.Process, pi.Thread, nil
}

// envBlock encodes env as a double-NUL-terminated UTF-16 environment block.
func envBlock(env []string) (*uint16, error) {
	var block []uint16
	for _, entry := range env {
		if strings.IndexByte(entry, 0) >= 0 {
			return nil, fmt.Errorf("environment entry contains NUL: %q", entry)
		}
		wide, err := windows.UTF16FromString(entry)
		if err != nil {
			return nil, err
		}
		block = append(block, wide...)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	block = append(block, 0)
	return &block[0], nil
}

// mergeEnv returns base with extra applied on top. Names match
// case-insensitively, as on Windows, and the last assignment wins.
func mergeEnv(base, extra []string) []string {
	merged := make([]string, 0, len(base)+len(extra))
	index := make(map[string]int, len(base)+len(extra))
	for _, entry := range append(append([]string(nil), base...), extra...) {
		key := strings.ToUpper(envName(entry))
		if i, ok := index[key]; ok {
			merged[i] = entry
			continue
		}
		index[key] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

// envName returns the variable name of a NAME=value entry. A leading "=" is
// part of the name (Windows keeps per-drive directories as =C:=C:\dir).
func envName(entry string) string {
	if i := strings.IndexByte(entry[min(1, len(entry)):], '='); i >= 0 {
		return entry[:i+1]
	}
	return entry
}

// runProcess runs spec to completion with its output copied to sink. A
// non-zero killJob receives the process, so ending ctx terminates the whole
// tree. After the process exits, output still held open by a surviving
// descendant is waited for at most pipeWaitDelay.
func runProcess(ctx context.Context, spec procSpec, killJob windows.Handle, sink io.Writer, warn func(format string, args ...any)) (processResult, error) {
	readEnd, writeEnd, err := newOutputPipe()
	if err != nil {
		return processResult{}, err
	}
	stdin, err := openNull()
	if err != nil {
		closeHandles(readEnd, writeEnd)
		return processResult{}, err
	}
	process, thread, err := createSuspended(spec, stdin, writeEnd)
	// The child holds its own copies; closing ours lets the reader see EOF
	// once the whole tree is gone.
	closeHandles(writeEnd, stdin)
	if err != nil {
		closeHandles(readEnd)
		return processResult{}, fmt.Errorf("starting process: %w", err)
	}
	if killJob != 0 {
		if err := windows.AssignProcessToJobObject(killJob, process); err != nil {
			warn("cannot track process tree: %v", err)
		}
	}
	if _, err := windows.ResumeThread(thread); err != nil {
		_ = windows.TerminateProcess(process, 1)
		closeHandles(thread, process, readEnd)
		return processResult{}, fmt.Errorf("resuming process: %w", err)
	}
	closeHandles(thread)

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		copyPipe(readEnd, sink)
	}()

	type exit struct {
		code uint32
		err  error
	}
	exited := make(chan exit, 1)
	go func() {
		defer closeHandles(process)
		if _, err := windows.WaitForSingleObject(process, windows.INFINITE); err != nil {
			exited <- exit{err: err}
			return
		}
		var code uint32
		err := windows.GetExitCodeProcess(process, &code)
		exited <- exit{code: code, err: err}
	}()

	var result processResult
	var ex exit
	select {
	case ex = <-exited:
	case <-ctx.Done():
		result.terminated = true
		terminateTree(killJob, process, warn)
		select {
		case ex = <-exited:
		case <-time.After(pipeWaitDelay):
			ex.err = errors.New("process did not exit after being terminated")
		}
	}

	select {
	case <-readDone:
	case <-time.After(pipeWaitDelay):
		abortReads(readEnd, readDone)
	}
	closeHandles(readEnd)
	result.exitCode = ex.code
	return result, ex.err
}

func terminateTree(killJob, process windows.Handle, warn func(format string, args ...any)) {
	if killJob != 0 {
		err := windows.TerminateJobObject(killJob, 1)
		if err == nil {
			return
		}
		warn("cannot terminate the job object, killing the main process only: %v", err)
	}
	if err := windows.TerminateProcess(process, 1); err != nil {
		warn("cannot terminate the process: %v", err)
	}
}

// copyPipe copies everything written to the pipe until it closes or the read
// is cancelled.
func copyPipe(read windows.Handle, w io.Writer) {
	buf := make([]byte, 32<<10)
	for {
		var n uint32
		err := windows.ReadFile(read, buf, &n, nil)
		if n > 0 {
			_, _ = w.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// abortReads cancels the blocked read until copyPipe has returned; a cancel
// that lands between two reads is lost, hence the retries.
func abortReads(read windows.Handle, done <-chan struct{}) {
	for {
		_ = windows.CancelIoEx(read, nil)
		select {
		case <-done:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}
