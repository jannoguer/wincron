package cron

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseRetryOption(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "* * * * * retry=3 a.exe\n* * * * * retry=2/30s b.exe\n* * * * * c.exe\n"))
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].Retries != 3 || jobs[0].RetryDelay != time.Minute {
		t.Errorf("retry=3: Retries %d, RetryDelay %s, want 3 and the 1m default", jobs[0].Retries, jobs[0].RetryDelay)
	}
	if jobs[1].Retries != 2 || jobs[1].RetryDelay != 30*time.Second {
		t.Errorf("retry=2/30s: Retries %d, RetryDelay %s, want 2 and 30s", jobs[1].Retries, jobs[1].RetryDelay)
	}
	if jobs[2].Retries != 0 {
		t.Errorf("no retry option: Retries %d, want 0", jobs[2].Retries)
	}
	if got := jobs[0].Options(); got != "retry=3" {
		t.Errorf("Options = %q, want retry=3", got)
	}
	if got := jobs[1].Options(); got != "retry=2/30s" {
		t.Errorf("Options = %q, want retry=2/30s", got)
	}
}

func TestParseRetryOptionErrors(t *testing.T) {
	for _, value := range []string{"0", "x", "-1", "2/0s", "2/soon", "2/30", "/30s"} {
		_, err := LoadFile(writeCrontab(t, "* * * * * retry="+value+" a.exe\n"))
		if err == nil || !strings.Contains(err.Error(), "retry=") {
			t.Errorf("retry=%s: err = %v, want a retry= error", value, err)
		}
	}
}

func countOf(log, text string) int {
	return strings.Count(log, text)
}

// syncBuffer is a log sink that is safe to read while a job goroutine writes.
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

func retryScheduler(t *testing.T, crontab string) (*Scheduler, *syncBuffer) {
	t.Helper()
	return retrySchedulerWithState(t, crontab, "")
}

func retrySchedulerWithState(t *testing.T, crontab, statePath string) (*Scheduler, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	return NewScheduler(writeCrontab(t, crontab), statePath, log.New(buf, "", 0)), buf
}

func waitForLog(t *testing.T, buf *syncBuffer, text string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(buf.String(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("log never contained %q:\n%s", text, buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRetryRerunsAFailingJobAndReportsOnce(t *testing.T) {
	s, buf := retryScheduler(t, "* * * * * retry=2/10ms exit 1\n")
	var failures []Failure
	var mu sync.Mutex
	s.OnFailure = func(f Failure) {
		mu.Lock()
		defer mu.Unlock()
		failures = append(failures, f)
	}
	s.launch(context.Background(), s.jobList()[0])
	s.wg.Wait()

	log := buf.String()
	if got := countOf(log, "CMD (SYSTEM) exit 1"); got != 3 {
		t.Errorf("ran %d times, want 1 run and 2 retries:\n%s", got, log)
	}
	if got := countOf(log, "RETRY (SYSTEM) exit 1: attempt "); got != 2 {
		t.Errorf("logged %d retries, want 2:\n%s", got, log)
	}
	if len(failures) != 1 || failures[0].Result != "exit status 1" {
		t.Errorf("failures = %+v, want one failure after the last attempt", failures)
	}
}

func TestRetryStopsOnceTheJobSucceeds(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	command := `if exist "` + marker + `" (exit 0) else (echo x>"` + marker + `" & exit 1)`
	s, buf := retryScheduler(t, "* * * * * retry=5/10ms "+command+"\n")
	s.launch(context.Background(), s.jobList()[0])
	s.wg.Wait()

	log := buf.String()
	if got := countOf(log, "CMD (SYSTEM)"); got != 2 {
		t.Errorf("ran %d times, want the failure and one successful retry:\n%s", got, log)
	}
	if got := countOf(log, "RETRY "); got != 1 {
		t.Errorf("logged %d retries, want 1:\n%s", got, log)
	}
}

func TestSuccessfulJobIsNotRetried(t *testing.T) {
	s, buf := retryScheduler(t, "* * * * * retry=3/10ms echo fine\n")
	s.launch(context.Background(), s.jobList()[0])
	s.wg.Wait()
	if strings.Contains(buf.String(), "RETRY") {
		t.Errorf("log %q: a successful job was retried", buf.String())
	}
}

func TestRetryHoldsTheOverlapClaimAndCountsAsOneRun(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	s, buf := retrySchedulerWithState(t, "* * * * * overlap=no retry=1/1h exit 1\n", statePath)
	job := s.jobList()[0]
	s.launch(context.Background(), job)
	waitForLog(t, buf, "RETRY")
	// The job is waiting to retry: its claim is still held.
	if s.claim(job.key) {
		t.Error("overlap claim was released while a retry was pending")
	}
	s.beginStop()
	s.wg.Wait()
	if !s.claim(job.key) {
		t.Error("overlap claim was not released when the job gave up")
	}
	history, err := LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if js, _ := history.Lookup(job); js.LastResult != "exit status 1" || js.LastStart.IsZero() {
		t.Errorf("state = %+v, want one recorded run with the failure", js)
	}
}

func TestShutdownCancelsPendingRetry(t *testing.T) {
	s, buf := retryScheduler(t, "* * * * * retry=3/1h exit 1\n")
	s.launch(context.Background(), s.jobList()[0])
	waitForLog(t, buf, "RETRY")
	s.beginStop()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a pending retry delayed shutdown")
	}
}
