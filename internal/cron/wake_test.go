package cron

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeWaker struct {
	armed   []time.Time
	disarms int
	err     error
}

func (f *fakeWaker) arm(at time.Time) error { f.armed = append(f.armed, at); return f.err }
func (f *fakeWaker) disarm() error          { f.disarms++; return f.err }
func (f *fakeWaker) close()                 {}

func TestParseWakeOption(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "* * * * * wake=yes a.exe\n* * * * * wake=no b.exe\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !jobs[0].Wake || jobs[1].Wake {
		t.Errorf("Wake = %v %v, want true false", jobs[0].Wake, jobs[1].Wake)
	}
	if got := jobs[0].Options(); got != "wake=yes" {
		t.Errorf("Options = %q, want wake=yes", got)
	}
	for _, text := range []string{"* * * * * wake=maybe a.exe\n", "@reboot wake=yes a.exe\n", "@logon wake=yes a.exe\n"} {
		if _, err := LoadFile(writeCrontab(t, text)); err == nil || !strings.Contains(err.Error(), "wake=") {
			t.Errorf("%q: err = %v, want a wake= error", text, err)
		}
	}
}

func TestUpdateWakeArmsEarliestWakeJob(t *testing.T) {
	s, _ := retryScheduler(t, "30 10 * * * wake=yes a.exe\n15 10 * * * wake=yes b.exe\n5 10 * * * c.exe\n")
	fake := &fakeWaker{}
	s.wake = fake
	now := at(2026, time.July, 6, 9, 0)
	s.updateWake(now)
	want := at(2026, time.July, 6, 10, 15)
	if len(fake.armed) != 1 || !fake.armed[0].Equal(want) {
		t.Fatalf("armed %v, want once at %v", fake.armed, want)
	}
	// Unchanged: no second call.
	s.updateWake(now.Add(time.Minute))
	if len(fake.armed) != 1 {
		t.Errorf("armed again with nothing changed: %v", fake.armed)
	}
	// After the earliest run, the next job is armed.
	s.updateWake(at(2026, time.July, 6, 10, 16))
	if len(fake.armed) != 2 || !fake.armed[1].Equal(at(2026, time.July, 6, 10, 30)) {
		t.Errorf("armed %v, want the 10:30 job next", fake.armed)
	}
}

func TestUpdateWakeDisarmsWhenNoWakeJobsRemain(t *testing.T) {
	s, _ := retryScheduler(t, "30 10 * * * wake=yes a.exe\n")
	fake := &fakeWaker{}
	s.wake = fake
	s.updateWake(at(2026, time.July, 6, 9, 0))
	s.jobsMu.Lock()
	s.jobs = nil
	s.jobsMu.Unlock()
	s.updateWake(at(2026, time.July, 6, 9, 1))
	if fake.disarms != 1 {
		t.Errorf("disarms = %d, want 1", fake.disarms)
	}
}

func TestUpdateWakeWarnsOnceOnFailure(t *testing.T) {
	s, buf := retryScheduler(t, "30 10 * * * wake=yes a.exe\n15 11 * * * wake=yes b.exe\n")
	s.wake = &fakeWaker{err: errors.New("denied")}
	s.updateWake(at(2026, time.July, 6, 9, 0))
	s.updateWake(at(2026, time.July, 6, 10, 31))
	if got := strings.Count(buf.String(), "WARN wake timer: denied"); got != 1 {
		t.Errorf("warned %d times, want 1:\n%s", got, buf.String())
	}
}

func TestWakeTimerArmsAndDisarms(t *testing.T) {
	w, err := newWakeTimer()
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	if err := w.arm(time.Now().Add(time.Hour)); err != nil {
		t.Errorf("arm: %v", err)
	}
	if err := w.disarm(); err != nil {
		t.Errorf("disarm: %v", err)
	}
}

func TestKeepAwakeSetsAndClearsRequest(t *testing.T) {
	release, err := keepAwake()
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Errorf("release: %v", err)
	}
}
