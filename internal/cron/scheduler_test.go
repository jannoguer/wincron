package cron

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testScheduler(t *testing.T, crontab string) (*Scheduler, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	s := NewScheduler(writeCrontab(t, crontab), logger)
	return s, &buf
}

func jobLines(jobs []Job) []int {
	var lines []int
	for _, j := range jobs {
		lines = append(lines, j.Line)
	}
	return lines
}

func TestCollectDueSingleMinute(t *testing.T) {
	s, _ := testScheduler(t, "30 10 * * * match.exe\n45 10 * * * other.exe\n")
	last := at(2026, time.July, 6, 10, 29)
	now := at(2026, time.July, 6, 10, 30)
	due, newLast := s.collectDue(last, now)
	if !newLast.Equal(now) {
		t.Errorf("newLast = %s, want %s", newLast, now)
	}
	if lines := jobLines(due); len(lines) != 1 || lines[0] != 1 {
		t.Errorf("due jobs at lines %v, want [1]", lines)
	}
}

func TestCollectDueClockNotAdvancing(t *testing.T) {
	s, _ := testScheduler(t, "* * * * * every.exe\n")
	last := at(2026, time.July, 6, 10, 30)
	for _, now := range []time.Time{last, last.Add(-time.Minute)} {
		due, newLast := s.collectDue(last, now)
		if len(due) != 0 {
			t.Errorf("now=%s: got %d due jobs, want 0", now, len(due))
		}
		if !newLast.Equal(last) {
			t.Errorf("now=%s: newLast = %s, want unchanged %s", now, newLast, last)
		}
	}
}

func TestCollectDueLateWakeRunsJobOnce(t *testing.T) {
	s, _ := testScheduler(t, "* * * * * every.exe\n")
	last := at(2026, time.July, 6, 10, 0)
	now := at(2026, time.July, 6, 10, 5)
	due, _ := s.collectDue(last, now)
	if len(due) != 1 {
		t.Errorf("got %d due jobs after 5-minute gap, want exactly 1", len(due))
	}
}

func TestCollectDueFindsMatchInsideGap(t *testing.T) {
	s, _ := testScheduler(t, "30 10 * * * inside.exe\n45 10 * * * outside.exe\n")
	last := at(2026, time.July, 6, 10, 28)
	now := at(2026, time.July, 6, 10, 32)
	due, _ := s.collectDue(last, now)
	if lines := jobLines(due); len(lines) != 1 || lines[0] != 1 {
		t.Errorf("due jobs at lines %v, want [1]", lines)
	}
}

func TestCollectDueCapsCatchup(t *testing.T) {
	s, buf := testScheduler(t, "* * * * * every.exe\n30 10 * * * early.exe\n30 11 * * * inside.exe\n")
	last := at(2026, time.July, 6, 9, 0)
	now := at(2026, time.July, 6, 12, 0)
	due, _ := s.collectDue(last, now)
	// Window is capped to the last maxCatchupMinutes: 10:30 is out, 11:30 is in.
	if lines := jobLines(due); len(lines) != 2 || lines[0] != 1 || lines[1] != 3 {
		t.Errorf("due jobs at lines %v, want [1 3]", lines)
	}
	if !strings.Contains(buf.String(), "skipping catch-up") {
		t.Errorf("log %q does not mention skipped catch-up", buf.String())
	}
}

func TestCollectDueSkipsRebootJobs(t *testing.T) {
	s, _ := testScheduler(t, "@reboot boot.exe\n* * * * * every.exe\n")
	due, _ := s.collectDue(at(2026, time.July, 6, 10, 0), at(2026, time.July, 6, 10, 1))
	if lines := jobLines(due); len(lines) != 1 || lines[0] != 2 {
		t.Errorf("due jobs at lines %v, want [2]", lines)
	}
}

func TestNewSchedulerMissingCrontab(t *testing.T) {
	var buf bytes.Buffer
	s := NewScheduler(filepath.Join(t.TempDir(), "missing.txt"), log.New(&buf, "", 0))
	if len(s.jobs) != 0 {
		t.Errorf("got %d jobs, want 0", len(s.jobs))
	}
	if !strings.Contains(buf.String(), "crontab unavailable") {
		t.Errorf("log %q does not mention unavailable crontab", buf.String())
	}
}

func TestReloadIfChanged(t *testing.T) {
	s, buf := testScheduler(t, "* * * * * one.exe\n")
	if len(s.jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(s.jobs))
	}
	base := time.Now()

	rewrite := func(content string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(s.crontabPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(s.crontabPath, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}

	// New mtime and valid content: reloaded.
	rewrite("* * * * * one.exe\n* * * * * two.exe\n", base.Add(time.Hour))
	s.reloadIfChanged()
	if len(s.jobs) != 2 {
		t.Fatalf("after valid edit: got %d jobs, want 2", len(s.jobs))
	}

	// Unchanged mtime: not reloaded even though content differs.
	rewrite("* * * * * one.exe\n", base.Add(time.Hour))
	s.reloadIfChanged()
	if len(s.jobs) != 2 {
		t.Errorf("mtime unchanged: got %d jobs, want 2 (no reload expected)", len(s.jobs))
	}

	// Invalid content: previous jobs kept.
	rewrite("not a valid line\n", base.Add(2*time.Hour))
	s.reloadIfChanged()
	if len(s.jobs) != 2 {
		t.Errorf("after invalid edit: got %d jobs, want 2", len(s.jobs))
	}
	if !strings.Contains(buf.String(), "reload failed") {
		t.Errorf("log %q does not mention failed reload", buf.String())
	}

	// File removed: previous jobs kept.
	if err := os.Remove(s.crontabPath); err != nil {
		t.Fatal(err)
	}
	s.reloadIfChanged()
	if len(s.jobs) != 2 {
		t.Errorf("after removal: got %d jobs, want 2", len(s.jobs))
	}
	if !strings.Contains(buf.String(), "stat failed") {
		t.Errorf("log %q does not mention stat failure", buf.String())
	}
}

func TestRunReturnsOnCancel(t *testing.T) {
	s, buf := testScheduler(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
	if !strings.Contains(buf.String(), "scheduler stopped") {
		t.Errorf("log %q does not mention scheduler stop", buf.String())
	}
}

func TestRunExecutesRebootJob(t *testing.T) {
	s, buf := testScheduler(t, "@reboot echo reboot-job-ran\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
	if !strings.Contains(buf.String(), "reboot-job-ran") {
		t.Errorf("log %q does not contain reboot job output", buf.String())
	}
}
