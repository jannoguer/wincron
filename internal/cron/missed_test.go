package cron

import (
	"strings"
	"testing"
	"time"
)

func TestParseMissedOption(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "0 5 * * * missed=run a.exe\n0 6 * * * missed=skip b.exe\n0 7 * * * c.exe\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !jobs[0].MissedRun || jobs[1].MissedRun || jobs[2].MissedRun {
		t.Errorf("MissedRun = %v %v %v, want true false false", jobs[0].MissedRun, jobs[1].MissedRun, jobs[2].MissedRun)
	}
	if got := jobs[0].Options(); got != "missed=run" {
		t.Errorf("Options = %q, want missed=run", got)
	}
}

func TestParseMissedOptionErrors(t *testing.T) {
	tests := map[string]string{
		"0 5 * * * missed=maybe a.exe":           "missed= requires run or skip",
		"0 5 * * * missed=run":                   "expected a command",
		"@reboot missed=run a.exe":               "does not apply to @reboot",
		"0 5 * * * missed=run missed=skip a.exe": "given twice",
	}
	for line, want := range tests {
		_, err := LoadFile(writeCrontab(t, line+"\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to contain %q", line, err, want)
		}
	}
}

func TestMissedJobs(t *testing.T) {
	// Daily at 05:00; "now" is Thursday 12:00, so the latest scheduled time
	// is Thursday 05:00.
	s, _ := testScheduler(t, "0 5 * * * missed=run daily.exe\n0 5 * * * plain.exe\n")
	now := at(2026, time.July, 9, 12, 0)
	seen := at(2026, time.July, 1, 0, 0)
	daily, plain := s.jobs[0], s.jobs[1]

	record := func(job Job, lastStart time.Time) {
		t.Helper()
		if err := s.state.recordStart(job, lastStart); err != nil {
			t.Fatal(err)
		}
	}
	setSeen := func(job Job, at time.Time) {
		t.Helper()
		js := s.state.jobs[job.key]
		js.FirstSeen = at
		s.state.jobs[job.key] = js
	}
	setSeen(daily, seen)
	setSeen(plain, seen)

	// Last ran Tuesday: Wednesday and Thursday 05:00 were missed.
	record(daily, at(2026, time.July, 7, 5, 0))
	record(plain, at(2026, time.July, 7, 5, 0))
	got := s.missedJobs(now, nil)
	if len(got) != 1 || got[0].key != daily.key {
		t.Errorf("missed = %v, want only the missed=run job (once, however many days were missed)", jobLines(got))
	}

	// Already due this tick: not reported twice.
	if got := s.missedJobs(now, []Job{daily}); len(got) != 0 {
		t.Errorf("missed = %v for a job already due, want none", jobLines(got))
	}

	// Ran at today's 05:00: nothing missed.
	record(daily, at(2026, time.July, 9, 5, 0))
	if got := s.missedJobs(now, nil); len(got) != 0 {
		t.Errorf("missed = %v after running on schedule, want none", jobLines(got))
	}
}

func TestMissedJobsNeverFiresOnFirstSight(t *testing.T) {
	s, _ := testScheduler(t, "0 5 * * * missed=run daily.exe\n")
	// The job was added at 10:00, after today's 05:00 slot had passed.
	js := s.state.jobs[s.jobs[0].key]
	js.FirstSeen = at(2026, time.July, 9, 10, 0)
	s.state.jobs[s.jobs[0].key] = js
	if got := s.missedJobs(at(2026, time.July, 9, 12, 0), nil); len(got) != 0 {
		t.Errorf("missed = %v for a job first seen after its last slot, want none", jobLines(got))
	}
	// The next day's slot is missed if the scheduler was down over it.
	if got := s.missedJobs(at(2026, time.July, 10, 12, 0), nil); len(got) != 1 {
		t.Errorf("missed = %v, want the job once its next slot passes unrun", jobLines(got))
	}
}

func TestMissedJobsIgnoresRunningOverlapNoJob(t *testing.T) {
	s, _ := testScheduler(t, "0 5 * * * missed=run overlap=no daily.exe\n")
	job := s.jobs[0]
	js := s.state.jobs[job.key]
	js.FirstSeen = at(2026, time.July, 1, 0, 0)
	js.LastStart = at(2026, time.July, 8, 5, 0)
	s.state.jobs[job.key] = js
	if !s.claim(job.key) {
		t.Fatal("could not claim the job")
	}
	if got := s.missedJobs(at(2026, time.July, 9, 12, 0), nil); len(got) != 0 {
		t.Errorf("missed = %v while the job is still running, want none", jobLines(got))
	}
}
