package main

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW, which x/sys/windows lacks.
type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	hwnd       windows.Handle
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instApp    windows.Handle
	idList     uintptr
	class      *uint16
	keyClass   windows.Handle
	hotKey     uint32
	icon       windows.Handle
	process    windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
)

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// runElevated runs this executable with args in an elevated process (a UAC
// prompt), waits for it, and returns its exit code.
func runElevated(args []string) (uint32, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	params, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{
		mask:       seeMaskNoCloseProcess | seeMaskNoAsync,
		verb:       verb,
		file:       file,
		parameters: params,
		show:       windows.SW_SHOWNORMAL,
	}
	info.size = uint32(unsafe.Sizeof(info))
	if r, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, errors.New("elevation was declined")
		}
		return 0, fmt.Errorf("starting elevated process: %w", callErr)
	}
	defer func() { _ = windows.CloseHandle(info.process) }()
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return 0, err
	}
	return code, nil
}

// elevatedLoad installs the crontab at src through an elevated
// "wincron load", so only the copy runs with administrator rights, never the
// editor.
func elevatedLoad(src string) error {
	code, err := runElevated([]string{"load", src})
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("elevated load failed with exit code %d", code)
	}
	return nil
}
