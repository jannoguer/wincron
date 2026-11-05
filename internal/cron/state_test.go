package cron

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "* * * * * a.exe\n* * * * * b.exe\n"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, time.July, 6, 9, 0, 0, 0, time.UTC)
	start := first.Add(time.Hour)
	st.sync(jobs, first)
	if err := st.recordStart(jobs[0], start); err != nil {
		t.Fatal(err)
	}
	if err := st.recordFinish(jobs[0], start.Add(2*time.Second), "exit 0"); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Lookup(jobs[0])
	if !ok || !got.FirstSeen.Equal(first) || !got.LastStart.Equal(start) || got.LastResult != "exit 0" || !got.LastFinish.Equal(start.Add(2*time.Second)) {
		t.Errorf("job a state = %+v (found %v), want the recorded run", got, ok)
	}
	other, ok := loaded.Lookup(jobs[1])
	if !ok || !other.LastStart.IsZero() || !other.FirstSeen.Equal(first) {
		t.Errorf("job b state = %+v (found %v), want first-seen only", other, ok)
	}
}

func TestStateSyncPrunesAndKeepsFirstSeen(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "* * * * * a.exe\n* * * * * b.exe\n"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := LoadState("")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, time.July, 6, 9, 0, 0, 0, time.UTC)
	if !st.sync(jobs, t0) {
		t.Error("first sync reported no change")
	}
	if st.sync(jobs, t0.Add(time.Hour)) {
		t.Error("syncing the same jobs reported a change")
	}
	if js, _ := st.Lookup(jobs[0]); !js.FirstSeen.Equal(t0) {
		t.Errorf("FirstSeen = %s, want it kept at %s", js.FirstSeen, t0)
	}
	if !st.sync(jobs[:1], t0.Add(2*time.Hour)) {
		t.Error("dropping a job reported no change")
	}
	if _, ok := st.Lookup(jobs[1]); ok {
		t.Error("state of the removed job was not pruned")
	}
}

func TestLoadStateMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	if st, err := LoadState(filepath.Join(dir, "none.json")); err != nil || st == nil {
		t.Errorf("missing file: (%v, %v), want an empty state", st, err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(bad)
	if err == nil {
		t.Error("corrupt file loaded without an error")
	}
	if st == nil {
		t.Error("corrupt file returned no usable empty state")
	}
}

func TestSchedulerRecordsRunInState(t *testing.T) {
	var buf bytes.Buffer
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := NewScheduler(writeCrontab(t, "* * * * * echo recorded\n"), statePath, log.New(&buf, "", 0))
	s.launch(context.Background(), s.jobs[0])
	s.wg.Wait()

	loaded, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	js, ok := loaded.Lookup(s.jobs[0])
	if !ok || js.LastStart.IsZero() || js.LastFinish.IsZero() || js.LastResult != "exit 0" {
		t.Errorf("state after a run = %+v (found %v), want start, finish and exit 0", js, ok)
	}
	if strings.Contains(buf.String(), "WARN") {
		t.Errorf("log %q has a warning", buf.String())
	}
}

func TestOnFailureHook(t *testing.T) {
	var buf bytes.Buffer
	s := NewScheduler(writeCrontab(t, "* * * * * echo oops& exit 3\n* * * * * echo fine\n"), "", log.New(&buf, "", 0))
	var failures []Failure
	var mu sync.Mutex
	s.OnFailure = func(f Failure) {
		mu.Lock()
		defer mu.Unlock()
		failures = append(failures, f)
	}
	for _, job := range s.jobs {
		s.launch(context.Background(), job)
	}
	s.wg.Wait()

	if len(failures) != 1 {
		t.Fatalf("got %d failures, want 1 (only the failing job)", len(failures))
	}
	f := failures[0]
	if f.Result != "exit status 3" || !strings.Contains(string(f.Output), "oops") || f.Job.Line != 1 {
		t.Errorf("failure = %+v, want exit status 3 with its output for line 1", f)
	}
}

func TestRunNowRunsAJobWithoutTouchingHistory(t *testing.T) {
	var buf bytes.Buffer
	statePath := filepath.Join(t.TempDir(), "state.json")
	s := NewScheduler(writeCrontab(t, "# comment\n* * * * * echo from-test-run\n"), statePath, log.New(&buf, "", 0))
	var stream bytes.Buffer
	result, err := s.RunNow(context.Background(), 2, &stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Summary != "exit 0" {
		t.Errorf("result = %+v, want exit 0", result)
	}
	if !strings.Contains(stream.String(), "from-test-run") {
		t.Errorf("stream %q does not carry the job output", stream.String())
	}
	if !strings.Contains(buf.String(), "CMD (SYSTEM) echo from-test-run") {
		t.Errorf("log %q does not record the run", buf.String())
	}
	if js, _ := s.state.Lookup(s.jobs[0]); !js.LastStart.IsZero() {
		t.Errorf("a test run changed the history: %+v", js)
	}
}

func TestRunNowUnknownLine(t *testing.T) {
	s, _ := testScheduler(t, "# comment\n* * * * * echo x\n")
	if _, err := s.RunNow(context.Background(), 1, io.Discard); err == nil || !strings.Contains(err.Error(), "no job on line 1") {
		t.Errorf("err = %v, want no job on line 1", err)
	}
}

func TestRunNowSeesAnEditedCrontab(t *testing.T) {
	s, _ := testScheduler(t, "* * * * * echo old\n")
	if err := os.WriteFile(s.crontabPath, []byte("* * * * * echo old\n* * * * * echo added\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().Add(time.Hour)
	if err := os.Chtimes(s.crontabPath, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	var stream bytes.Buffer
	if _, err := s.RunNow(context.Background(), 2, &stream); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stream.String(), "added") {
		t.Errorf("stream %q: the new line was not picked up", stream.String())
	}
}
