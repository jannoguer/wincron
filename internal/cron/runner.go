package cron

import (
	"context"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const maxJobOutputBytes = 64 << 10

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

	out := cappedBuffer{max: maxJobOutputBytes}
	cmd.Stdout = &out
	cmd.Stderr = &out

	logger.Printf("start job L%d: %s", job.Line, job.Command)
	started := time.Now()
	err := cmd.Run()
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
