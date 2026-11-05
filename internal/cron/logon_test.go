package cron

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseLogonJob(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "@logon echo hi\n@LOGON user=jan echo jan\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || !jobs[0].Logon || jobs[0].Reboot || jobs[0].Spec != "@logon" || jobs[1].User != "jan" || !jobs[1].Logon {
		t.Errorf("jobs = %+v, want two @logon jobs", jobs)
	}
	for line, want := range map[string]string{
		"@logon":                  "@logon requires a command",
		"@logon user=jan":         "@logon requires a command",
		"@logon missed=run a.exe": "does not apply to @logon",
	} {
		if _, err := LoadFile(writeCrontab(t, line+"\n")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to contain %q", line, err, want)
		}
	}
}

func TestCollectDueSkipsLogonJobs(t *testing.T) {
	s, _ := testScheduler(t, "@logon logon.exe\n* * * * * every.exe\n")
	due, _ := s.collectDue(at(2026, time.July, 6, 10, 0), at(2026, time.July, 6, 10, 1))
	if lines := jobLines(due); len(lines) != 1 || lines[0] != 2 {
		t.Errorf("due jobs at lines %v, want [2]", lines)
	}
}

func TestRunLogonStartsMatchingJobs(t *testing.T) {
	s, buf := testScheduler(t, "@logon echo any-user\n@logon user=jan echo only-jan\n@logon user=bob echo only-bob\n* * * * * echo scheduled\n")
	s.setJobCtx(context.Background())
	s.RunLogon(func(user string) bool { return user == "jan" })
	s.wg.Wait()

	log := buf.String()
	// The user= job is skipped here (no SYSTEM), but its SKIP line proves it
	// was started for this logon.
	for _, want := range []string{"CMD (SYSTEM) echo any-user", "(jan) echo only-jan"} {
		if !strings.Contains(log, want) {
			t.Errorf("log %q does not contain %q", log, want)
		}
	}
	for _, unwanted := range []string{"only-bob", "scheduled"} {
		if strings.Contains(log, unwanted) {
			t.Errorf("log %q contains %q, which should not have run", log, unwanted)
		}
	}
}

func TestRunLogonIgnoredWhenNotRunning(t *testing.T) {
	s, buf := testScheduler(t, "@logon echo any-user\n")
	s.RunLogon(func(string) bool { return true })
	s.wg.Wait()
	if strings.Contains(buf.String(), "CMD") {
		t.Errorf("log %q: a job started while the scheduler was not running", buf.String())
	}
}

func TestLogonJobsWarnOnceWithoutTheService(t *testing.T) {
	s, buf := testScheduler(t, "@logon echo hi\n")
	s.warnIfLogonJobsCannotRun()
	s.warnIfLogonJobsCannotRun()
	if got := strings.Count(buf.String(), "@logon jobs only run when wincron runs as the service"); got != 1 {
		t.Errorf("warned %d times, want once; log %q", got, buf.String())
	}
	s2, buf2 := testScheduler(t, "@logon echo hi\n")
	s2.LogonTriggers = true
	s2.warnIfLogonJobsCannotRun()
	if buf2.Len() != 0 {
		t.Errorf("log %q: warned although the host delivers logon events", buf2.String())
	}
}
