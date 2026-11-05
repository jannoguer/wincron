package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"wincron/internal/cron"
)

// editor returns the command used to edit the crontab: VISUAL, then EDITOR,
// then notepad, as cron does.
func editor() string {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return "notepad"
}

// editorCmdLine builds the cmd.exe command line that runs the editor on
// path. Like cron, the editor goes through a shell, so it may carry arguments
// (code --wait) and may be a .cmd shim. /S makes cmd strip only the outer
// quotes.
func editorCmdLine(comspec, editor, path string) string {
	return `"` + comspec + `" /S /C "` + editor + ` "` + path + `""`
}

func runEditor(path string) error {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd"
	}
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: editorCmdLine(comspec, editor(), path)}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running editor %q: %w", editor(), err)
	}
	return nil
}

// editSession edits a private copy of the crontab and installs it only once
// it validates, so a half-finished edit never reaches the running service.
type editSession struct {
	crontabPath string
	runEditor   func(path string) error
	install     func(tmpPath string) error
	in          io.Reader
	out         io.Writer
}

func edit(crontabPath string) error {
	return (&editSession{
		crontabPath: crontabPath,
		runEditor:   runEditor,
		install: func(tmp string) error {
			err := loadCrontab(crontabPath, tmp)
			if errors.Is(err, os.ErrPermission) {
				fmt.Println("access denied, asking for elevation to install the crontab")
				return elevatedLoad(tmp)
			}
			return err
		},
		in:  os.Stdin,
		out: os.Stdout,
	}).run()
}

func (s *editSession) run() error {
	original, err := os.ReadFile(s.crontabPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.CreateTemp("", "wincron-*.txt")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_, werr := tmp.Write(original)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return errors.Join(werr, os.Remove(tmpPath))
	}

	input := bufio.NewReader(s.in)
	for {
		if err := s.runEditor(tmpPath); err != nil {
			return s.keep(tmpPath, err)
		}
		edited, err := os.ReadFile(tmpPath)
		if err != nil {
			return s.keep(tmpPath, err)
		}
		if bytes.Equal(edited, original) {
			s.printf("no changes made to crontab\n")
			return os.Remove(tmpPath)
		}
		if _, err := cron.Parse(edited); err != nil {
			s.printf("wincron: %v\n", err)
			if s.askRetry(input) {
				continue
			}
			return s.keep(tmpPath, errors.New("crontab not installed"))
		}
		if err := s.install(tmpPath); err != nil {
			return s.keep(tmpPath, err)
		}
		s.printf("crontab installed\n")
		return os.Remove(tmpPath)
	}
}

// printf writes to the console; a failed write has no recovery.
func (s *editSession) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.out, format, args...)
}

func (s *editSession) askRetry(input *bufio.Reader) bool {
	s.printf("Do you want to retry the same edit? (y/n) ")
	answer, _ := input.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

// keep reports where the edited copy was left, so the work is not lost.
func (s *editSession) keep(tmpPath string, err error) error {
	return fmt.Errorf("%w (your edits are kept in %s)", err, tmpPath)
}

// loadCrontab validates the file at src and atomically replaces crontabPath
// with its content. An invalid src leaves crontabPath untouched.
func loadCrontab(crontabPath, src string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if _, err := cron.Parse(data); err != nil {
		return err
	}
	return replaceFile(crontabPath, data)
}

// replaceFile writes data to a temporary file next to path and moves it over
// path, so a reader never sees a partial file.
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".crontab-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_, err = tmp.Write(data)
	if serr := tmp.Sync(); err == nil {
		err = serr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = moveReplace(tmpPath, path)
	}
	if err != nil {
		return errors.Join(err, os.Remove(tmpPath))
	}
	return nil
}

func moveReplace(from, to string) error {
	fromPtr, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPtr, toPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func load(crontabPath string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: wincron load <file>")
	}
	if err := loadCrontab(crontabPath, args[0]); err != nil {
		return err
	}
	fmt.Println("crontab installed")
	return nil
}
