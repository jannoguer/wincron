package cron

import (
	"bytes"
	"context"
	"log"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestCappedBuffer(t *testing.T) {
	b := cappedBuffer{max: 10}
	for _, chunk := range []string{"12345", "678", "90", "overflow"} {
		n, err := b.Write([]byte(chunk))
		if err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = (%d, %v), want (%d, nil)", chunk, n, err, len(chunk))
		}
	}
	if got := string(b.buf); got != "1234567890" {
		t.Errorf("buf = %q, want first 10 bytes only", got)
	}
	if b.total != 18 {
		t.Errorf("total = %d, want 18", b.total)
	}
}

func TestCappedBufferPartialChunk(t *testing.T) {
	b := cappedBuffer{max: 4}
	b.Write([]byte("ab"))
	b.Write([]byte("cdef"))
	if got := string(b.buf); got != "abcd" {
		t.Errorf("buf = %q, want %q", got, "abcd")
	}
	if b.total != 6 {
		t.Errorf("total = %d, want 6", b.total)
	}
}

func runTestJob(t *testing.T, ctx context.Context, job Job) string {
	t.Helper()
	var buf bytes.Buffer
	runJob(ctx, job, log.New(&buf, "", 0))
	return buf.String()
}

func TestRunJobCapturesOutput(t *testing.T) {
	got := runTestJob(t, context.Background(), Job{Command: "echo hello wincron", Line: 3})
	for _, want := range []string{"start job L3: echo hello wincron", "output job L3", "hello wincron", "finish job L3: exit 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q does not contain %q", got, want)
		}
	}
}

func TestRunJobReportsExitCode(t *testing.T) {
	got := runTestJob(t, context.Background(), Job{Command: "exit 7", Line: 1})
	if !strings.Contains(got, "finish job L1: exit status 7") {
		t.Errorf("log %q does not report exit status 7", got)
	}
}

func TestRunJobPassesEnv(t *testing.T) {
	job := Job{Command: "echo value=%WINCRON_TEST_VAR%", Line: 1, Envs: []string{"WINCRON_TEST_VAR=magic123"}}
	got := runTestJob(t, context.Background(), job)
	if !strings.Contains(got, "value=magic123") {
		t.Errorf("log %q does not contain expanded env value", got)
	}
}

func TestRunJobTruncatesLongOutput(t *testing.T) {
	cmd := `powershell -NoProfile -Command "Write-Output ('x'*100000)"`
	got := runTestJob(t, context.Background(), Job{Command: cmd, Line: 2})
	if !strings.Contains(got, "output job L2 truncated:") {
		t.Errorf("log %q does not mention truncation", got)
	}
	if !strings.Contains(got, "finish job L2: exit 0") {
		t.Errorf("log %q does not report success", got)
	}
}

func TestRunJobCancelDoesNotHang(t *testing.T) {
	// cmd.exe spawns ping as a child that inherits the output pipes; without
	// tree termination plus WaitDelay this test hangs for ~30 seconds.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan string, 1)
	go func() {
		done <- runTestJob(t, ctx, Job{Command: "ping -n 30 127.0.0.1", Line: 4})
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case got := <-done:
		if !strings.Contains(got, "finish job L4") {
			t.Errorf("log %q does not contain finish line", got)
		}
	case <-time.After(pipeWaitDelay + 10*time.Second):
		t.Fatal("runJob did not return after cancellation")
	}
}

// Tests run-as-user path. Needs SE_TCB (run under LocalSystem).
func TestRunJobAsUserEndToEnd(t *testing.T) {
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !self.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Skip("requires LocalSystem (run with psexec -s)")
	}
	var token windows.Token
	if err := windows.WTSQueryUserToken(windows.WTSGetActiveConsoleSessionId(), &token); err != nil {
		t.Skipf("no user on console session: %v", err)
	}
	tokenUser, err := token.GetTokenUser()
	token.Close()
	if err != nil {
		t.Fatal(err)
	}
	account, _, _, err := tokenUser.User.Sid.LookupAccount("")
	if err != nil {
		t.Fatal(err)
	}

	// The sentinel must come from the child's environment; matching the bare
	// account name would be satisfied by runJob's own "start job as" line.
	job := Job{Command: `echo RUNAS:%USERNAME%& if /I "%CD%"=="%USERPROFILE%" echo IN-PROFILE`, Line: 9, User: account}
	got := runTestJob(t, context.Background(), job)
	if !strings.Contains(strings.ToLower(got), strings.ToLower("RUNAS:"+account)) {
		t.Errorf("log %q does not show the job running as %q", got, account)
	}
	if !strings.Contains(got, "IN-PROFILE") {
		t.Errorf("log %q does not show the job starting in the user profile", got)
	}
	if !strings.Contains(got, "exit 0") {
		t.Errorf("log %q does not report success", got)
	}
}

func TestJobObjectTerminatesProcess(t *testing.T) {
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(job)
	if err := assignToJobObject(job, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := windows.TerminateJobObject(job, 1); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("terminated process reported exit 0")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("process still running after TerminateJobObject")
	}
}
