package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
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
	s, _ := newTestService(t, "# no jobs\n")
	return startTestService(s)
}

// newTestService returns a service over a temporary crontab, and the log
// path it writes to.
func newTestService(t *testing.T, crontab string) (*cronService, string) {
	t.Helper()
	dir := t.TempDir()
	crontabPath := filepath.Join(dir, "crontab.txt")
	if err := os.WriteFile(crontabPath, []byte(crontab), 0644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "wincron.log")
	return &cronService{crontabPath: crontabPath, logPath: logPath}, logPath
}

func startTestService(s *cronService) (chan svc.ChangeRequest, chan svc.Status, chan struct{}) {
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

func TestServiceRunsLogonJobsOnSessionLogon(t *testing.T) {
	s, logPath := newTestService(t, "@logon echo logon-any\n@logon user=jan echo logon-jan\n@logon user=bob echo logon-bob\n")
	var gotSession uint32
	s.sessionUser = func(session uint32) (func(string) bool, func(), error) {
		gotSession = session
		return func(user string) bool { return user == "jan" }, func() {}, nil
	}
	requests, status, done := startTestService(s)
	running := waitForRunning(t, status)
	if running.Accepts&svc.AcceptSessionChange == 0 {
		t.Errorf("Accepts = %b, want AcceptSessionChange set", running.Accepts)
	}

	notification := windows.WTSSESSION_NOTIFICATION{Size: uint32(unsafe.Sizeof(windows.WTSSESSION_NOTIFICATION{})), SessionID: 42}
	requests <- svc.ChangeRequest{Cmd: svc.SessionChange, EventType: windows.WTS_SESSION_LOGON, EventData: uintptr(unsafe.Pointer(&notification))}
	// A logoff event must not start anything.
	requests <- svc.ChangeRequest{Cmd: svc.SessionChange, EventType: windows.WTS_SESSION_LOGOFF, EventData: uintptr(unsafe.Pointer(&notification))}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	<-done

	if gotSession != 42 {
		t.Errorf("session = %d, want 42", gotSession)
	}
	log := readFile(t, logPath)
	// Without SYSTEM the user= job is skipped at token lookup, but its SKIP
	// line proves it was started for the logon.
	for _, want := range []string{"CMD (SYSTEM) echo logon-any", "(jan) echo logon-jan"} {
		if got := strings.Count(log, want); got != 1 {
			t.Errorf("%q appears %d times in the log, want 1:\n%s", want, got, log)
		}
	}
	if strings.Contains(log, "logon-bob") {
		t.Errorf("log %q: the job of another user ran", log)
	}
}
