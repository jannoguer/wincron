package cron

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxJobOutputBytes = 64 << 10

// Bounds the wait for output pipes held open by descendants that outlive
// cmd.exe, and the wait for an unresponsive process after cancellation.
// Without it, Wait blocks until every inherited pipe handle is closed.
const pipeWaitDelay = 5 * time.Second

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

func runJob(ctx context.Context, job Job, logger *log.Logger) {
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd"
	}
	cmd := exec.CommandContext(ctx, shell)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "/C " + job.Command}
	cmd.WaitDelay = pipeWaitDelay
	if job.User != "" {
		token, env, err := userContext(job.User)
		if err != nil {
			logger.Printf("skip job L%d: run as %q: %v", job.Line, job.User, err)
			return
		}
		defer func() { _ = token.Close() }()
		cmd.SysProcAttr.Token = syscall.Token(token)
		// Keep the console hidden.
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
		cmd.Env = append(env, job.Envs...)
		if profile := envValue(env, "USERPROFILE"); profile != "" {
			cmd.Dir = profile
		}
	} else if len(job.Envs) > 0 {
		cmd.Env = append(os.Environ(), job.Envs...)
	}

	out := cappedBuffer{max: maxJobOutputBytes}
	cmd.Stdout = &out
	cmd.Stderr = &out

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
			logger.Printf("job L%d: cannot set kill-on-close, descendants may outlive the run: %v", job.Line, err)
		}
		// Start suspended: a child spawned before assignment to the job
		// object would escape tree termination.
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	}

	if job.User != "" {
		logger.Printf("start job L%d as %s: %s", job.Line, job.User, job.Command)
	} else {
		logger.Printf("start job L%d: %s", job.Line, job.Command)
	}
	started := time.Now()
	err := cmd.Start()
	if err == nil {
		if killJobErr == nil {
			if aerr := assignToJobObject(killJob, cmd.Process.Pid); aerr != nil {
				logger.Printf("job L%d: cannot track process tree: %v", job.Line, aerr)
			}
			if rerr := resumeMainThread(cmd.Process.Pid); rerr != nil {
				logger.Printf("job L%d: cannot resume suspended process, killing: %v", job.Line, rerr)
				_ = cmd.Process.Kill()
			}
		}
		err = cmd.Wait()
	}
	duration := time.Since(started).Round(time.Millisecond)

	if len(out.buf) > 0 {
		logger.Printf("output job L%d:\n%s", job.Line, out.buf)
		if out.total > int64(len(out.buf)) {
			logger.Printf("output job L%d truncated: %d of %d bytes shown", job.Line, len(out.buf), out.total)
		}
	}
	if err != nil {
		logger.Printf("finish job L%d: %v (%s)", job.Line, err, duration)
		return
	}
	logger.Printf("finish job L%d: exit 0 (%s)", job.Line, duration)
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
