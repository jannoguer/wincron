package cron

import (
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

func newYork(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestCollectDueClockMovedBackRunsOnlyWildcardJobs(t *testing.T) {
	s, buf := testScheduler(t, "* * * * * every.exe\n0 12 * * * noon.exe\n")
	last := at(2026, time.July, 6, 12, 0)
	now := last.Add(-time.Hour)
	due, newLast := s.collectDue(last, now)
	if lines := jobLines(due); len(lines) != 1 || lines[0] != 1 {
		t.Errorf("due jobs at lines %v on the jump, want only the wildcard job [1]", lines)
	}
	if !newLast.Equal(now) {
		t.Errorf("newLast = %s, want %s", newLast, now)
	}
	if !strings.Contains(buf.String(), "clock moved back 1h0m0s") {
		t.Errorf("log %q does not report the backward jump", buf.String())
	}
	// Replaying up to the minute it came from still skips the noon job.
	last = now
	for minute := now.Add(time.Minute); !minute.After(at(2026, time.July, 6, 12, 0)); minute = minute.Add(time.Minute) {
		due, last = s.collectDue(last, minute)
		if lines := jobLines(due); len(lines) != 1 || lines[0] != 1 {
			t.Fatalf("due jobs at lines %v at %s while replaying, want [1]", lines, minute.Format("15:04"))
		}
	}
	due, _ = s.collectDue(last, last.Add(time.Minute))
	if lines := jobLines(due); len(lines) != 1 || lines[0] != 1 {
		t.Errorf("due jobs at lines %v after catching up, want [1]", lines)
	}
}

func TestCollectDueClockMovedBackFarResumesFromNewTime(t *testing.T) {
	s, buf := testScheduler(t, "* * * * * every.exe\n")
	last := at(2026, time.July, 6, 12, 0)
	now := last.Add(-3 * time.Hour)
	due, newLast := s.collectDue(last, now)
	if len(due) != 0 || !newLast.Equal(now) {
		t.Errorf("got %d due jobs and newLast %s, want none and %s", len(due), newLast, now)
	}
	if !strings.Contains(buf.String(), "resuming from") {
		t.Errorf("log %q does not report the reset", buf.String())
	}
	due, _ = s.collectDue(newLast, now.Add(time.Minute))
	if len(due) != 1 {
		t.Errorf("got %d due jobs the minute after the reset, want 1", len(due))
	}
}

func TestCollectDueSpringForwardRunsSkippedJobsOnce(t *testing.T) {
	loc := newYork(t)
	s, _ := testScheduler(t, "30 2 * * * skipped.exe\n0 3 * * * three.exe\n30 3 * * * later.exe\n* * * * * every.exe\n")
	last := time.Date(2026, time.March, 8, 1, 59, 0, 0, loc)
	now := last.Add(time.Minute)
	if now.Hour() != 3 {
		t.Fatalf("test setup: %s is not after the DST jump", now)
	}
	due, _ := s.collectDue(last, now)
	if lines := jobLines(due); len(lines) != 3 || lines[0] != 1 || lines[1] != 2 || lines[2] != 4 {
		t.Errorf("due jobs at lines %v at the jump, want [1 2 4]", lines)
	}
}

func TestCollectDueSpringForwardWithLateWake(t *testing.T) {
	loc := newYork(t)
	s, buf := testScheduler(t, "15 2 * * * skipped.exe\n")
	last := time.Date(2026, time.March, 8, 1, 30, 0, 0, loc)
	now := time.Date(2026, time.March, 8, 3, 30, 0, 0, loc) // one hour later in real time
	due, _ := s.collectDue(last, now)
	if len(due) != 1 {
		t.Errorf("got %d due jobs, want the skipped-hour job once", len(due))
	}
	if strings.Contains(buf.String(), "catch-up") {
		t.Errorf("log %q: an hour of real time must not trigger the catch-up cap", buf.String())
	}
}

func TestCollectDueFallBackRunsOnlyWildcardJobsAgain(t *testing.T) {
	loc := newYork(t)
	s, _ := testScheduler(t, "30 1 * * * once.exe\n0 * * * * hourly.exe\n* * * * * every.exe\n0 2 * * * two.exe\n")
	last := time.Date(2026, time.November, 1, 5, 59, 0, 0, time.UTC).In(loc) // 01:59 EDT
	counts := make(map[int]int)
	// 01:00 EST through 02:59 EST.
	for range 120 {
		now := last.Add(time.Minute)
		due, _ := s.collectDue(last, now)
		for _, job := range due {
			counts[job.Line]++
		}
		last = now
	}
	if counts[1] != 0 {
		t.Errorf("01:30 job ran %d times in the repeated hour, want 0", counts[1])
	}
	if counts[2] != 2 {
		t.Errorf("hourly job ran %d times, want 2 (01:00 EST and 02:00 EST)", counts[2])
	}
	if counts[3] != 120 {
		t.Errorf("every-minute job ran %d times, want 120", counts[3])
	}
	if counts[4] != 1 {
		t.Errorf("02:00 job ran %d times, want once", counts[4])
	}
}

func TestScheduleWildcardFlags(t *testing.T) {
	tests := []struct {
		spec string
		want bool
	}{
		{"* * * * *", true},
		{"*/5 3 * * *", true},
		{"0 * * * *", true},
		{"0 */2 * * *", true},
		{"0 0 * * *", false},
		{"0,30 1-5 * * *", false},
	}
	for _, tt := range tests {
		sched, err := ParseSchedule(strings.Fields(tt.spec))
		if err != nil {
			t.Fatal(err)
		}
		if got := sched.isWildcard(); got != tt.want {
			t.Errorf("isWildcard(%q) = %v, want %v", tt.spec, got, tt.want)
		}
	}
}
