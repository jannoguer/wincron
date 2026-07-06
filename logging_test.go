package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRotatingWriterRotatesAtMaxSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	w, err := newRotatingWriter(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	first := strings.Repeat("a", 60)
	second := strings.Repeat("b", 60)
	third := strings.Repeat("c", 60)

	if _, err := w.Write([]byte(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("rotated file exists before max size was reached")
	}

	if _, err := w.Write([]byte(second)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".1"); got != first {
		t.Errorf("rotated file = %q, want first write", got)
	}
	if got := readFile(t, path); got != second {
		t.Errorf("current file = %q, want second write", got)
	}

	if _, err := w.Write([]byte(third)); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".1"); got != second {
		t.Errorf("rotated file = %q, want second write (only one old file kept)", got)
	}
	if got := readFile(t, path); got != third {
		t.Errorf("current file = %q, want third write", got)
	}
}

func TestRotatingWriterFirstOversizedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	w, err := newRotatingWriter(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// A single write larger than the limit into an empty file must not
	// rotate first, or nothing would ever be written.
	big := strings.Repeat("x", 50)
	if _, err := w.Write([]byte(big)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("empty file was rotated")
	}
	if got := readFile(t, path); got != big {
		t.Errorf("current file = %q, want full write", got)
	}

	if _, err := w.Write([]byte("y")); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".1"); got != big {
		t.Errorf("rotated file = %q, want oversized first write", got)
	}
	if got := readFile(t, path); got != "y" {
		t.Errorf("current file = %q, want %q", got, "y")
	}
}

func TestRotatingWriterResumesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	existing := strings.Repeat("e", 90)
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	w, err := newRotatingWriter(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Write([]byte(strings.Repeat("n", 20))); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".1"); got != existing {
		t.Errorf("rotated file = %q, want pre-existing content", got)
	}
	if got := readFile(t, path); got != strings.Repeat("n", 20) {
		t.Errorf("current file = %q, want new write", got)
	}
}

func TestRotatingWriterFailureCooldown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	w, err := newRotatingWriter(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	// A recent rotation failure suppresses further attempts for the cooldown,
	// so an over-limit write must append instead of rotating.
	w.lastFail = time.Now()
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatal("rotation happened during failure cooldown")
	}
	if got := readFile(t, path); got != "0123456789abc" {
		t.Errorf("current file = %q, want appended content", got)
	}

	// Once the cooldown has passed, rotation resumes.
	w.lastFail = time.Now().Add(-2 * rotateRetryCooldown)
	if _, err := w.Write([]byte("z")); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".1"); got != "0123456789abc" {
		t.Errorf("rotated file = %q, want previous content", got)
	}
}

func TestOpenLogger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	logger, closer, err := openLogger(path, false)
	if err != nil {
		t.Fatal(err)
	}
	logger.Print("logger smoke test")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); !strings.Contains(got, "logger smoke test") {
		t.Errorf("log file %q does not contain message", got)
	}
}
