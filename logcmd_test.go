package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLastLines(t *testing.T) {
	tests := []struct {
		name string
		data string
		n    int
		want string
	}{
		{"fewer lines than asked", "a\nb\n", 5, "a\nb\n"},
		{"exact", "a\nb\n", 2, "a\nb\n"},
		{"last one", "a\nb\nc\n", 1, "c\n"},
		{"last two", "a\nb\nc\n", 2, "b\nc\n"},
		{"no trailing newline", "a\nb\nc", 2, "b\nc"},
		{"zero", "a\nb\n", 0, ""},
		{"empty", "", 3, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(lastLines([]byte(tt.data), tt.n)); got != tt.want {
				t.Errorf("lastLines(%q, %d) = %q, want %q", tt.data, tt.n, got, tt.want)
			}
		})
	}
}

func TestShowLogPrintsTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wincron.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := showLog(context.Background(), path, &out, 2, false); err != nil {
		t.Fatal(err)
	}
	if out.String() != "two\nthree\n" {
		t.Errorf("output %q, want the last two lines", out.String())
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitFor(t *testing.T, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("output %q never contained %q", out.String(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFollowSurvivesRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wincron.log")
	appendTo(t, path, "old\n")
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- showLog(ctx, path, out, 10, true) }()
	waitFor(t, out, "old\n")

	appendTo(t, path, "appended\n")
	waitFor(t, out, "appended\n")

	// The service rotates by renaming the log while a follower has it open.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rotating a followed log: %v", err)
	}
	appendTo(t, path, "after rotation\n")
	waitFor(t, out, "after rotation\n")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("showLog returned %v after cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("showLog did not return after cancel")
	}
}
