package cron

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxJobOutputBytes = 64 << 10

// Bounds the wait for output pipes held open by descendants that outlive
// cmd.exe, and the wait for an unresponsive process after cancellation.
// Without it, Wait blocks until every inherited pipe handle is closed.
const pipeWaitDelay = 5 * time.Second

// tolerantWriter discards write errors, so a stream that went away (a closed
// pipe) cannot stall the job's output copy.
type tolerantWriter struct{ w io.Writer }

func (t tolerantWriter) Write(p []byte) (int, error) {
	_, _ = t.w.Write(p)
	return len(p), nil
}

type cappedBuffer struct {
	buf   []byte
	max   int
	total int64
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.total += int64(len(p))
	if remaining := b.max - len(b.buf); remaining > 0 {
		if len(p) > remaining {
			b.buf = append(b.buf, p[:remaining]...)
		} else {
			b.buf = append(b.buf, p...)
		}
	}
	return len(p), nil
}

// label names a job in the log the way cronie does: the account in
// parentheses, then the command.
func (j Job) label() string {
	account := "SYSTEM"
	if j.User != "" {
		account = j.User
	}
	return "(" + account + ") " + j.Command
}

// RunResult is the outcome of one run.
type RunResult struct {
	OK      bool
	Summary string // "exit 0", "exit status 7", "timed out after 1m0s", or why it did not run
	Output  []byte // what the job wrote, as UTF-8, capped at maxJobOutputBytes
}

// runJob runs job to completion and logs it. A non-nil stream also receives
// the output as it is written; a failing stream does not stop the job.
func runJob(ctx context.Context, job Job, logger *log.Logger, stream io.Writer) RunResult {
	if job.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, job.Timeout)
		defer cancel()
	}
	warn := func(format string, args ...any) {
		logger.Printf("WARN %s: %s", job.label(), fmt.Sprintf(format, args...))
	}
	shell, err := shellPath()
	if err != nil {
		logger.Printf("END %s: %v", job.label(), err)
		return RunResult{Summary: err.Error()}
	}
	spec := procSpec{
		appName: shell,
		// /S makes cmd strip just the outer quotes, leaving a command that
		// starts with a quoted path intact.
		cmdLine: `"` + shell + `" /S /C "` + job.Command + `"`,
	}
	if job.User != "" {
		session, err := userContext(job.User)
		if err != nil {
			logger.Printf("SKIP %s: %v", job.label(), err)
			return RunResult{Summary: "skipped: " + err.Error()}
		}
		defer func() {
			if err := session.close(); err != nil {
				warn("releasing the user session: %v", err)
			}
		}()
		spec.token = session.token
		// lpDesktop must be set explicitly: a service has no window station
		// in the user's session.
		spec.desktop = &session.desktop
		// Keep the console hidden.
		spec.flags |= windows.CREATE_NO_WINDOW
		spec.env = mergeEnv(session.env, job.Envs)
		spec.dir = envValue(session.env, "USERPROFILE")
	} else {
		if len(job.Envs) > 0 {
			spec.env = mergeEnv(os.Environ(), job.Envs)
		}
		// Like cron's HOME: start in the scheduler account's profile, not
		// System32.
		if home, err := os.UserHomeDir(); err == nil {
			spec.dir = home
		} else {
			warn("no home directory, starting in the scheduler's working directory: %v", err)
		}
	}

	out := cappedBuffer{max: maxJobOutputBytes}
	var sink io.Writer = &out
	if stream != nil {
		sink = io.MultiWriter(&out, tolerantWriter{stream})
	}

	// A job object tracks the whole process tree: cancellation terminates it,
	// and kill-on-close reaps survivors when the deferred CloseHandle runs
	// (or the service itself dies).
	var killJob windows.Handle
	if handle, err := windows.CreateJobObject(nil, nil); err != nil {
		warn("cannot create a job object, descendants may outlive the run: %v", err)
	} else {
		killJob = handle
		defer func() { _ = windows.CloseHandle(killJob) }()
		if err := setKillOnClose(killJob); err != nil {
			warn("cannot set kill-on-close, descendants may outlive the run: %v", err)
		}
	}

	logger.Printf("CMD %s", job.label())
	started := time.Now()
	result, err := runProcess(ctx, spec, killJob, sink, warn)
	duration := time.Since(started).Round(time.Millisecond)

	var output []byte
	if len(out.buf) > 0 {
		truncated := out.total > int64(len(out.buf))
		if truncated {
			out.buf = trimPartialRune(out.buf)
		}
		output = decodeOutput(out.buf)
		logger.Printf("OUT %s:\n%s", job.label(), output)
		if truncated {
			logger.Printf("OUT %s: truncated, %d of %d bytes shown", job.label(), len(out.buf), out.total)
		}
	}
	switch {
	case err != nil:
		logger.Printf("END %s: %v (%s)", job.label(), err, duration)
		return RunResult{Summary: err.Error(), Output: output}
	case result.terminated && job.Timeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded):
		logger.Printf("END %s: timed out after %s", job.label(), job.Timeout)
		return RunResult{Summary: "timed out after " + job.Timeout.String(), Output: output}
	case result.terminated:
		logger.Printf("END %s: canceled (%s)", job.label(), duration)
		return RunResult{Summary: "canceled", Output: output}
	case result.exitCode != 0:
		summary := fmt.Sprintf("exit status %d", result.exitCode)
		logger.Printf("END %s: %s (%s)", job.label(), summary, duration)
		return RunResult{Summary: summary, Output: output}
	}
	logger.Printf("END %s: exit 0 (%s)", job.label(), duration)
	return RunResult{OK: true, Summary: "exit 0", Output: output}
}

// interactiveDesktop is the logged-on user's visible desktop.
const interactiveDesktop = `winsta0\default`

// shellPath returns the command interpreter jobs run under.
func shellPath() (string, error) {
	if shell := os.Getenv("ComSpec"); shell != "" {
		return shell, nil
	}
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return "", fmt.Errorf("locating cmd.exe: %w", err)
	}
	return dir + `\cmd.exe`, nil
}

// setKillOnClose configures the job object to terminate every process it
// contains as soon as its last handle closes.
func setKillOnClose(job windows.Handle) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	return err
}

var procGetOEMCP = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")

// decodeOutput returns job output as UTF-8. cmd.exe and most console tools
// write in the OEM code page, so bytes that are not valid UTF-8 are converted
// from it; valid UTF-8 is left alone.
func decodeOutput(b []byte) []byte {
	if utf8.Valid(b) {
		return b
	}
	codePage, _, _ := procGetOEMCP.Call()
	return codePageToUTF8(b, uint32(codePage))
}

// trimPartialRune drops a final UTF-8 character cut short by the output cap,
// so it does not make otherwise valid UTF-8 look like OEM text.
func trimPartialRune(b []byte) []byte {
	for i := 1; i < utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			break
		}
	}
	return b
}

func codePageToUTF8(b []byte, codePage uint32) []byte {
	n, err := windows.MultiByteToWideChar(codePage, 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n == 0 {
		return b
	}
	wide := make([]uint16, n)
	if _, err := windows.MultiByteToWideChar(codePage, 0, &b[0], int32(len(b)), &wide[0], n); err != nil {
		return b
	}
	return []byte(string(utf16.Decode(wide)))
}
