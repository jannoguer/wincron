package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wincron/internal/cron"
)

func TestWriteStatus(t *testing.T) {
	dir := t.TempDir()
	crontab := filepath.Join(dir, "crontab.txt")
	content := "0 5 * * 1 user=jan timeout=5m overlap=no backup.exe --full\n@reboot boot.exe\n0 0 30 2 * never.exe\n"
	if err := os.WriteFile(crontab, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	jobs, err := cron.LoadFile(crontab)
	if err != nil {
		t.Fatal(err)
	}
	history, err := cron.LoadState("")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writeStatus(&out, statusReport{
		service: "running",
		paths:   paths{crontab: crontab, log: "wincron.log", state: "state.json"},
		jobs:    jobs,
		history: history,
		now:     time.Date(2026, time.July, 6, 12, 0, 0, 0, time.Local),
	})
	got := out.String()
	for _, want := range []string{
		"service  running",
		"3 jobs",
		"line 1  0 5 * * 1  backup.exe --full",
		"user     jan",
		"options  user=jan timeout=5m0s overlap=no",
		"next     2026-07-13 05:00",
		"last     never run",
		"line 2  @reboot  boot.exe",
		"next     when the service starts",
		"next     never",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status output does not contain %q:\n%s", want, got)
		}
	}
}

func TestWriteStatusReportsCrontabError(t *testing.T) {
	var out bytes.Buffer
	writeStatus(&out, statusReport{service: "stopped", jobsErr: os.ErrNotExist})
	if !strings.Contains(out.String(), "crontab error:") {
		t.Errorf("status output %q does not report the crontab error", out.String())
	}
}

func TestLastRun(t *testing.T) {
	start := time.Date(2026, time.July, 6, 5, 0, 0, 0, time.Local)
	tests := []struct {
		name string
		js   cron.JobState
		want string
	}{
		{"never", cron.JobState{}, "never run"},
		{"finished", cron.JobState{LastStart: start, LastFinish: start.Add(3 * time.Second), LastResult: "exit 0"}, "2026-07-06 05:00, exit 0 (took 3s)"},
		{"first run in progress", cron.JobState{LastStart: start}, "2026-07-06 05:00, running or no result recorded"},
		{"later run in progress", cron.JobState{LastStart: start, LastFinish: start.Add(-time.Hour), LastResult: "exit 0"}, "2026-07-06 05:00, running or no result recorded"},
	}
	for _, tt := range tests {
		if got := lastRun(tt.js); got != tt.want {
			t.Errorf("%s: lastRun = %q, want %q", tt.name, got, tt.want)
		}
	}
}
