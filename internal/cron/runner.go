package cron

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"syscall"
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
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd"
	}
	cmd := exec.CommandContext(ctx, shell)
	// /S makes cmd strip just the outer quotes, leaving a command that
	// starts with a quoted path intact.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/S /C "` + job.Command + `"`}
	cmd.WaitDelay = pipeWaitDelay
	if job.User != "" {
		token, env, err := userContext(job.User)
		if err != nil {
			logger.Printf("SKIP %s: %v", job.label(), err)
			return RunResult{Summary: "skipped: " + err.Error()}
		}
		defer func() { _ = token.Close() }()
		cmd.SysProcAttr.Token = syscall.Token(token)
		// Keep the console hidden.
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
		cmd.Env = append(env, job.Envs...)
		if profile := envValue(env, "USERPROFILE"); profile != "" {
			cmd.Dir = profile
		}
	} else {
		if len(job.Envs) > 0 {
			cmd.Env = append(os.Environ(), job.Envs...)
		}
		// Like cron's HOME: start in the scheduler account's profile, not
		// System32.
		if home, err := os.UserHomeDir(); err == nil {
			cmd.Dir = home
		} else {
			logger.Printf("WARN %s: no home directory, starting in the scheduler's working directory: %v", job.label(), err)
		}
	}

	out := cappedBuffer{max: maxJobOutputBytes}
	var sink io.Writer = &out
	if stream != nil {
		sink = io.MultiWriter(&out, tolerantWriter{stream})
	}
	cmd.Stdout = sink
	cmd.Stderr = sink

	// A job object tracks the whole process tree: cancellation terminates it,
	// and kill-on-close reaps survivors when the deferred CloseHandle runs
	// (or the service itself dies). Kill remains the fallback.
	killJob, killJobErr := windows.CreateJobObject(nil, nil)
	if killJobErr == nil {
		defer func() { _ = windows.CloseHandle(killJob) }()
		cmd.Cancel = func() error {
			_ = windows.TerminateJobObject(killJob, 1)
			return cmd.Process.Kill()
		}
		if err := setKillOnClose(killJob); err != nil {
			logger.Printf("WARN %s: cannot set kill-on-close, descendants may outlive the run: %v", job.label(), err)
		}
		// Start suspended: a child spawned before assignment to the job
		// object would escape tree termination.
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	}

	logger.Printf("CMD %s", job.label())
	started := time.Now()
	err := cmd.Start()
	if err == nil {
		if killJobErr == nil {
			if aerr := assignToJobObject(killJob, cmd.Process.Pid); aerr != nil {
				logger.Printf("WARN %s: cannot track process tree: %v", job.label(), aerr)
			}
			if rerr := resumeMainThread(cmd.Process.Pid); rerr != nil {
				logger.Printf("WARN %s: cannot resume suspended process, killing: %v", job.label(), rerr)
				_ = cmd.Process.Kill()
			}
		}
		err = cmd.Wait()
	}
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
	if err != nil {
		if job.Timeout > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			logger.Printf("END %s: timed out after %s", job.label(), job.Timeout)
			return RunResult{Summary: "timed out after " + job.Timeout.String(), Output: output}
		}
		logger.Printf("END %s: %v (%s)", job.label(), err, duration)
		return RunResult{Summary: err.Error(), Output: output}
	}
	logger.Printf("END %s: exit 0 (%s)", job.label(), duration)
	return RunResult{OK: true, Summary: "exit 0", Output: output}
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

func assignToJobObject(job windows.Handle, pid int) error {
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(proc) }()
	return windows.AssignProcessToJobObject(job, proc)
}

// resumeMainThread resumes every thread owned by pid, releasing a process
// started with CREATE_SUSPENDED.
func resumeMainThread(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	found := false
	for terr := windows.Thread32First(snapshot, &te); terr == nil; terr = windows.Thread32Next(snapshot, &te) {
		if te.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, oerr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if oerr != nil {
			return oerr
		}
		_, rerr := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if rerr != nil {
			return rerr
		}
		found = true
	}
	if !found {
		return fmt.Errorf("no threads found for pid %d", pid)
	}
	return nil
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
