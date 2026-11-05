package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const crontabTemplate = `# e.g.: run a backup of all your user accounts at 5 a.m every week with:
# 0 5 * * 1 tar -zcf C:\backups\home.tgz C:\Users

`

const (
	envKeyPath      = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	wmSettingChange = 0x001A
	hwndBroadcast   = 0xFFFF
	smtoAbortIfHung = 0x0002
)

var procSendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// installDir is where the service runs from. It is under Program Files so
// that only administrators can replace the binary a SYSTEM service executes.
func installDir() (string, error) {
	pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", fmt.Errorf("locating Program Files: %w", err)
	}
	return filepath.Join(pf, "wincron"), nil
}

func requireElevated(action string) error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return nil
	}
	return fmt.Errorf("%s needs an elevated shell: open an administrator terminal, or run 'sudo wincron %s' on Windows 11", action, action)
}

// install copies this executable to Program Files, registers the service
// against that copy, puts the directory on the machine PATH and starts the
// service. Running it again upgrades in place and keeps crontab.txt.
func install(exePath string) error {
	if err := requireElevated("install"); err != nil {
		return err
	}
	dir, err := installDir()
	if err != nil {
		return err
	}
	if err := stopService(); err != nil &&
		!errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) && !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return fmt.Errorf("stopping the running service: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	installed, err := copyExe(exePath, dir)
	if err != nil {
		return err
	}
	if err := ensureCrontab(filepath.Join(dir, "crontab.txt")); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "uninstall.bat")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := installService(installed); err != nil {
		return err
	}
	if err := editMachinePath(func(path string) (string, bool) { return addPathEntry(path, dir) }); err != nil {
		return err
	}
	if err := startService(); err != nil {
		return err
	}
	fmt.Printf("installed to %s and started the service\n", dir)
	fmt.Println("open a new terminal to pick up the PATH change")
	return nil
}

// copyExe copies src into dir as wincron.exe and returns the new path. The
// copy lands under a temporary name first, so a failure never leaves a
// truncated executable behind.
func copyExe(src, dir string) (string, error) {
	dst := filepath.Join(dir, "wincron.exe")
	if same, err := sameFile(src, dst); err != nil {
		return "", err
	} else if same {
		return dst, nil
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(dir, ".wincron-*.exe")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(tmp, in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = moveReplace(tmp.Name(), dst)
	}
	if err != nil {
		return "", errors.Join(fmt.Errorf("copying wincron.exe to %s: %w", dir, err), os.Remove(tmp.Name()))
	}
	return dst, nil
}

func sameFile(a, b string) (bool, error) {
	infoB, err := os.Stat(b)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	infoA, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	return os.SameFile(infoA, infoB), nil
}

// ensureCrontab writes the template crontab unless one already exists.
func ensureCrontab(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(crontabTemplate)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// uninstall stops and removes the service, the PATH entry and wincron's own
// files. crontab.txt is kept, and the directory only goes once it is empty.
func uninstall(exePath string) error {
	if err := requireElevated("uninstall"); err != nil {
		return err
	}
	dir, err := installDir()
	if err != nil {
		return err
	}
	if err := uninstallService(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return err
	}
	if err := editMachinePath(func(path string) (string, bool) { return removePathEntry(path, dir) }); err != nil {
		return err
	}
	if err := removeOwnFiles(dir); err != nil {
		return err
	}
	installed := filepath.Join(dir, "wincron.exe")
	self, err := sameFile(exePath, installed)
	if err != nil {
		return err
	}
	if self {
		// A running executable cannot delete itself; a detached shell does
		// it once this process has exited.
		err = deleteAfterExit(installed, dir)
	} else if err = removeIfExists(installed); err == nil {
		err = removeDirIfEmpty(dir)
	}
	if err != nil {
		return err
	}
	fmt.Printf("uninstalled; crontab.txt in %s was kept\n", dir)
	return nil
}

// ownFiles are the files wincron creates next to its executable, other than
// the executable and crontab.txt.
var ownFiles = []string{"wincron.log", "wincron.log.1", "wincron-state.json", "uninstall.bat"}

func removeOwnFiles(dir string) error {
	for _, name := range ownFiles {
		if err := removeIfExists(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func removeDirIfEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return nil
	}
	return os.Remove(dir)
}

// deleteAfterExit starts a detached shell that waits a moment, deletes exe,
// and removes dir if that leaves it empty (rd refuses a non-empty one).
func deleteAfterExit(exe, dir string) error {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd"
	}
	script := fmt.Sprintf(`ping -n 3 127.0.0.1 >nul & del /f /q "%s" & rd "%s"`, exe, dir)
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `"` + comspec + `" /S /C "` + script + `"`,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("scheduling removal of %s: %w", exe, err)
	}
	return cmd.Process.Release()
}

// addPathEntry appends dir to a semicolon-separated PATH unless an entry
// already names it, and reports whether it changed anything.
func addPathEntry(path, dir string) (string, bool) {
	if pathIndex(path, dir) >= 0 {
		return path, false
	}
	if path == "" || strings.HasSuffix(path, ";") {
		return path + dir, true
	}
	return path + ";" + dir, true
}

// removePathEntry drops every entry naming dir from a PATH value.
func removePathEntry(path, dir string) (string, bool) {
	entries := strings.Split(path, ";")
	kept := entries[:0]
	for _, e := range entries {
		if !samePathEntry(e, dir) {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(entries) {
		return path, false
	}
	return strings.Join(kept, ";"), true
}

func pathIndex(path, dir string) int {
	for i, e := range strings.Split(path, ";") {
		if samePathEntry(e, dir) {
			return i
		}
	}
	return -1
}

func samePathEntry(entry, dir string) bool {
	return strings.EqualFold(strings.TrimRight(entry, `\`), strings.TrimRight(dir, `\`))
}

// editMachinePath rewrites the machine-wide PATH with edit, keeping it
// REG_EXPAND_SZ, and tells running programs to reload their environment. It
// does nothing when edit reports no change.
func editMachinePath(edit func(path string) (string, bool)) error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, envKeyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening the machine environment: %w", err)
	}
	defer func() { _ = key.Close() }()
	path, _, err := key.GetStringValue("Path")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("reading the machine PATH: %w", err)
	}
	updated, changed := edit(path)
	if !changed {
		return nil
	}
	if err := key.SetExpandStringValue("Path", updated); err != nil {
		return fmt.Errorf("writing the machine PATH: %w", err)
	}
	return broadcastEnvironmentChange()
}

// broadcastEnvironmentChange sends WM_SETTINGCHANGE so Explorer and new
// terminals pick up the PATH change without a sign-out.
func broadcastEnvironmentChange() error {
	env, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return err
	}
	var result uintptr
	r, _, callErr := procSendMessageTimeout.Call(
		hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)),
		smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
	if r == 0 {
		if callErr == windows.ERROR_SUCCESS {
			// A timeout leaves no error code; the registry write already
			// happened, so new processes see the change regardless.
			return nil
		}
		return fmt.Errorf("broadcasting the environment change: %w", callErr)
	}
	return nil
}
