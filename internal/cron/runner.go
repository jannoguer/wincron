package cron

import (
	"context"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"

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
	if len(job.Envs) > 0 {
		cmd.Env = append(os.Environ(), job.Envs...)
	}

	out := cappedBuffer{max: maxJobOutputBytes}
	cmd.Stdout = &out
	cmd.Stderr = &out

	// Killing cmd.exe alone leaves its children running, so on cancellation
	// terminate the whole tree through a job object. Kill remains the
	// fallback in case termination fails or assignment never happened.
	killJob, killJobErr := windows.CreateJobObject(nil, nil)
	if killJobErr == nil {
		defer windows.CloseHandle(killJob)
		cmd.Cancel = func() error {
			_ = windows.TerminateJobObject(killJob, 1)
			return cmd.Process.Kill()
		}
	}

	logger.Printf("start job L%d: %s", job.Line, job.Command)
	started := time.Now()
	err := cmd.Start()
	if err == nil {
		if killJobErr == nil {
			if aerr := assignToJobObject(killJob, cmd.Process.Pid); aerr != nil {
				logger.Printf("job L%d: cannot track process tree: %v", job.Line, aerr)
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

func assignToJobObject(job windows.Handle, pid int) error {
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(proc)
	return windows.AssignProcessToJobObject(job, proc)
}
