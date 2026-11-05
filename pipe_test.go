package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"wincron/internal/cron"
)

// everyoneSDDL lets the test client, which is not an administrator, connect.
const everyoneSDDL = "D:(A;;GA;;;WD)"

func startPipeServer(t *testing.T, run func(ctx context.Context, line int, stream io.Writer) (cron.RunResult, error)) (name string, stop func()) {
	t.Helper()
	name = fmt.Sprintf(`\\.\pipe\wincron-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	var logs bytes.Buffer
	server := &pipeServer{name: name, sddl: everyoneSDDL, run: run, logger: log.New(&logs, "", 0)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.serve(ctx) }()
	// Give the first pipe instance time to exist.
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, err := openPipe(name)
		if err == nil {
			// Connected as a client; send nothing and let the handler fail.
			_ = f.Close()
			break
		}
		select {
		case serr := <-done:
			t.Fatalf("server stopped: %v", serr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never came up: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return name, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serve did not return after cancel")
		}
	}
}

func TestPipeStreamsOutputAndResult(t *testing.T) {
	name, stop := startPipeServer(t, func(_ context.Context, line int, stream io.Writer) (cron.RunResult, error) {
		_, _ = fmt.Fprintf(stream, "line %d says hello\n", line)
		_, _ = io.WriteString(stream, "second chunk\n")
		return cron.RunResult{OK: true, Summary: "exit 0"}, nil
	})
	defer stop()

	var out bytes.Buffer
	if err := runViaService(name, 4, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"line 4 says hello\nsecond chunk\n", "job finished: exit 0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q does not contain %q", out.String(), want)
		}
	}
}

func TestPipeReportsFailureAndErrors(t *testing.T) {
	name, stop := startPipeServer(t, func(_ context.Context, line int, _ io.Writer) (cron.RunResult, error) {
		if line == 9 {
			return cron.RunResult{}, errors.New("no job on line 9")
		}
		return cron.RunResult{Summary: "exit status 7"}, nil
	})
	defer stop()

	var out bytes.Buffer
	if err := runViaService(name, 1, &out); err == nil || !strings.Contains(err.Error(), "job failed: exit status 7") {
		t.Errorf("failed job: err = %v, want job failed: exit status 7", err)
	}
	// The accept loop keeps serving after the first client.
	if err := runViaService(name, 9, &out); err == nil || !strings.Contains(err.Error(), "no job on line 9") {
		t.Errorf("unknown line: err = %v, want the server's message", err)
	}
}

func TestOpenPipeWithoutServer(t *testing.T) {
	_, err := openPipe(`\\.\pipe\wincron-test-nobody-listens`)
	if err == nil || !strings.Contains(err.Error(), "is it running") {
		t.Errorf("err = %v, want a hint that the service is not running", err)
	}
}

func TestReadRequest(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"RUN 3\n", 3, false},
		{"RUN 12\r\n", 12, false},
		{"RUN 0\n", 0, true},
		{"RUN x\n", 0, true},
		{"STOP 3\n", 0, true},
		{"", 0, true},
	}
	for _, tt := range tests {
		got, err := readRequest(strings.NewReader(tt.in))
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("readRequest(%q) = (%d, %v), want (%d, error %v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}
