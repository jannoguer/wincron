package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/windows/svc"

	"wincron/internal/cron"
)

func waitForRunning(t *testing.T, status <-chan svc.Status) svc.Status {
	t.Helper()
	for {
		select {
		case s := <-status:
			if s.State == svc.Running {
				return s
			}
		case <-time.After(30 * time.Second):
			t.Fatal("service never reported Running")
		}
	}
}

func runTestService(t *testing.T) (chan svc.ChangeRequest, chan svc.Status, chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	crontabPath := filepath.Join(dir, "crontab.txt")
	if err := os.WriteFile(crontabPath, []byte("# no jobs\n"), 0644); err != nil {
		t.Fatal(err)
	}
	s := &cronService{crontabPath: crontabPath, logPath: filepath.Join(dir, "wincron.log")}

	requests := make(chan svc.ChangeRequest, 1)
	status := make(chan svc.Status, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.Execute(nil, requests, status)
	}()
	return requests, status, done
}

func TestServiceAcceptsPreShutdown(t *testing.T) {
	requests, status, done := runTestService(t)
	running := waitForRunning(t, status)
	if running.Accepts&svc.AcceptPreShutdown == 0 {
		t.Errorf("Accepts = %b, want AcceptPreShutdown set", running.Accepts)
	}
	if running.Accepts&svc.AcceptStop == 0 {
		t.Errorf("Accepts = %b, want AcceptStop set", running.Accepts)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	<-done
}

func TestServiceStopsOnEveryStopCommand(t *testing.T) {
	commands := map[string]svc.Cmd{"Stop": svc.Stop, "Shutdown": svc.Shutdown, "PreShutdown": svc.PreShutdown}
	for name, cmd := range commands {
		t.Run(name, func(t *testing.T) {
			requests, status, done := runTestService(t)
			waitForRunning(t, status)
			requests <- svc.ChangeRequest{Cmd: cmd}
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatalf("service did not stop on %s", name)
			}
		})
	}
}

func TestFormatFailure(t *testing.T) {
	f := cron.Failure{Job: cron.Job{Command: "backup.exe", User: "jan"}, Result: "exit status 7", Output: []byte("disk full\n")}
	got := formatFailure(f)
	for _, want := range []string{"backup.exe", "user: jan", "result: exit status 7", "disk full"} {
		if !strings.Contains(got, want) {
			t.Errorf("message %q does not contain %q", got, want)
		}
	}
	if got := formatFailure(cron.Failure{Job: cron.Job{Command: "x"}, Result: "timed out"}); !strings.Contains(got, "user: SYSTEM") || strings.Contains(got, "output") {
		t.Errorf("message %q: want SYSTEM and no output section", got)
	}
}

func TestTailBytes(t *testing.T) {
	long := []byte("first line\nsecond line\nthird line\n")
	if got := string(tailBytes(long, 1000)); got != string(long) {
		t.Errorf("short input changed: %q", got)
	}
	if got := string(tailBytes(long, 20)); got != "third line\n" {
		t.Errorf("tailBytes cut = %q, want to start at a line", got)
	}
	multibyte := []byte("aaa\u00e9\u00e9\u00e9")
	if got := tailBytes(multibyte, 3); !utf8.Valid(got) {
		t.Errorf("tailBytes split a character: %q", got)
	}
}
