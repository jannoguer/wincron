package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
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
