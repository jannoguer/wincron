package cron

import (
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestMergeEnv(t *testing.T) {
	base := []string{`Path=C:\bin`, `=C:=C:\dir`, "USERNAME=jan"}
	extra := []string{`PATH=C:\other`, "NEW=1", "new=2"}
	want := []string{`PATH=C:\other`, `=C:=C:\dir`, "USERNAME=jan", "new=2"}
	if got := mergeEnv(base, extra); !reflect.DeepEqual(got, want) {
		t.Errorf("mergeEnv = %q, want %q", got, want)
	}
	if got := mergeEnv(base, nil); !reflect.DeepEqual(got, base) {
		t.Errorf("mergeEnv with nothing to add = %q, want %q", got, base)
	}
}

func TestEnvBlockRoundTrip(t *testing.T) {
	env := []string{"A=1", "B=wörld"}
	block, err := envBlock(env)
	if err != nil {
		t.Fatal(err)
	}
	if got := parseEnvBlock(block); !reflect.DeepEqual(got, env) {
		t.Errorf("parseEnvBlock(envBlock(%q)) = %q", env, got)
	}
	if _, err := envBlock([]string{"A=1\x00B=2"}); err == nil {
		t.Error("envBlock accepted an entry with a NUL")
	}
}

func TestCreateSuspendedHoldsProcessUntilResumed(t *testing.T) {
	shell, err := shellPath()
	if err != nil {
		t.Fatal(err)
	}
	readEnd, writeEnd, err := newOutputPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandles(readEnd)
	stdin, err := openNull()
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandles(stdin)
	process, thread, err := createSuspended(procSpec{appName: shell, cmdLine: `"` + shell + `" /S /C "echo started& exit 5"`}, stdin, writeEnd)
	closeHandles(writeEnd)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandles(process, thread)

	if event, _ := windows.WaitForSingleObject(process, 300); event != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("suspended process finished before being resumed (wait result %d)", event)
	}
	if _, err := windows.ResumeThread(thread); err != nil {
		t.Fatal(err)
	}
	if event, err := windows.WaitForSingleObject(process, 10000); event != windows.WAIT_OBJECT_0 {
		t.Fatalf("process did not exit after resume: %d, %v", event, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil || code != 5 {
		t.Errorf("exit code = %d, %v; want 5", code, err)
	}
	var out [64]byte
	var n uint32
	if err := windows.ReadFile(readEnd, out[:], &n, nil); err != nil || string(out[:n]) != "started\r\n" {
		t.Errorf("output %q, %v; want the echo through the pipe", out[:n], err)
	}
}

func TestRunProcessReportsExitCodeAndOutput(t *testing.T) {
	shell, err := shellPath()
	if err != nil {
		t.Fatal(err)
	}
	var out cappedBuffer
	out.max = 1024
	result, err := runProcess(t.Context(), procSpec{appName: shell, cmdLine: `"` + shell + `" /S /C "echo hi& exit 3"`}, 0, &out, t.Logf)
	if err != nil || result.exitCode != 3 || result.terminated {
		t.Errorf("runProcess = (%+v, %v), want exit code 3", result, err)
	}
	if string(out.buf) != "hi\r\n" {
		t.Errorf("output %q, want hi", out.buf)
	}
}
