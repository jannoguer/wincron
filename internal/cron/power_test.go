package cron

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParsePowerOption(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "* * * * * power=ac a.exe\n* * * * * power=any b.exe\n* * * * * c.exe\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !jobs[0].ACOnly || jobs[1].ACOnly || jobs[2].ACOnly {
		t.Errorf("ACOnly = %v %v %v, want true false false", jobs[0].ACOnly, jobs[1].ACOnly, jobs[2].ACOnly)
	}
	if got := jobs[0].Options(); got != "power=ac" {
		t.Errorf("Options = %q, want power=ac", got)
	}
	if _, err := LoadFile(writeCrontab(t, "* * * * * power=battery a.exe\n")); err == nil || !strings.Contains(err.Error(), "power=") {
		t.Errorf("power=battery: err = %v, want a power= error", err)
	}
}

func TestPowerACSkipsOnBattery(t *testing.T) {
	for _, battery := range []bool{true, false} {
		s, buf := retryScheduler(t, "* * * * * power=ac echo ran\n* * * * * echo always\n")
		s.onBattery = func() bool { return battery }
		for _, job := range s.jobList() {
			s.launch(context.Background(), job)
		}
		s.wg.Wait()
		log := buf.String()
		if got := strings.Contains(log, "SKIP (SYSTEM) echo ran: on battery"); got != battery {
			t.Errorf("on battery %v: skip logged %v:\n%s", battery, got, log)
		}
		if got := strings.Contains(log, "CMD (SYSTEM) echo ran"); got == battery {
			t.Errorf("on battery %v: job ran %v:\n%s", battery, got, log)
		}
		if !strings.Contains(log, "CMD (SYSTEM) echo always") {
			t.Errorf("on battery %v: a job without power=ac did not run:\n%s", battery, log)
		}
	}
}

func TestMissedACJobWaitsForMainsPower(t *testing.T) {
	s, _ := testScheduler(t, "0 5 * * * missed=run power=ac daily.exe\n")
	job := s.jobs[0]
	js := s.state.jobs[job.key]
	js.FirstSeen = at(2026, time.July, 1, 0, 0)
	js.LastStart = at(2026, time.July, 7, 5, 0)
	s.state.jobs[job.key] = js
	now := at(2026, time.July, 9, 12, 0)

	s.onBattery = func() bool { return true }
	if got := s.missedJobs(now, nil); len(got) != 0 {
		t.Errorf("missed = %v on battery, want none until mains power", jobLines(got))
	}
	s.onBattery = func() bool { return false }
	if got := s.missedJobs(now, nil); len(got) != 1 {
		t.Errorf("missed = %v on mains power, want the job once", jobLines(got))
	}
}
