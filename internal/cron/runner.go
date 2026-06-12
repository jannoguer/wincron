package cron

import (
	"context"
	"log"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func runJob(ctx context.Context, job Job, logger *log.Logger) {
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd"
	}
	cmd := exec.CommandContext(ctx, shell)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "/C " + job.Command}

	logger.Printf("start job L%d: %s", job.Line, job.Command)
	started := time.Now()
	output, err := cmd.CombinedOutput()
	duration := time.Since(started).Round(time.Millisecond)

	if len(output) > 0 {
		logger.Printf("output job L%d:\n%s", job.Line, output)
	}
	if err != nil {
		logger.Printf("finish job L%d: %v (%s)", job.Line, err, duration)
		return
	}
	logger.Printf("finish job L%d: exit 0 (%s)", job.Line, duration)
}
