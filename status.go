package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/windows"

	"wincron/internal/cron"
)

const statusTimeFormat = "2006-01-02 15:04"

// serviceState reports the service's state using only query rights, so it
// works without an elevated shell.
func serviceState() (string, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "", fmt.Errorf("connecting to the service manager: %w", err)
	}
	defer func() { _ = windows.CloseServiceHandle(scm) }()
	name, err := windows.UTF16PtrFromString(serviceName)
	if err != nil {
		return "", err
	}
	handle, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return "not installed", nil
	}
	if err != nil {
		return "", fmt.Errorf("opening the service: %w", err)
	}
	defer func() { _ = windows.CloseServiceHandle(handle) }()
	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(handle, &status); err != nil {
		return "", fmt.Errorf("querying the service: %w", err)
	}
	switch status.CurrentState {
	case windows.SERVICE_RUNNING:
		return "running", nil
	case windows.SERVICE_STOPPED:
		return "stopped", nil
	case windows.SERVICE_START_PENDING:
		return "starting", nil
	case windows.SERVICE_STOP_PENDING:
		return "stopping", nil
	default:
		return fmt.Sprintf("state %d", status.CurrentState), nil
	}
}

func status(p paths) error {
	state, err := serviceState()
	if err != nil {
		state = "unknown (" + err.Error() + ")"
	}
	jobs, jobsErr := cron.LoadFile(p.crontab)
	history, historyErr := cron.LoadState(p.state)
	writeStatus(os.Stdout, statusReport{
		service: state,
		paths:   p,
		jobs:    jobs,
		jobsErr: jobsErr,
		history: history,
		now:     time.Now(),
	})
	if historyErr != nil {
		fprintf(os.Stderr, "wincron: ignoring the state file: %v\n", historyErr)
	}
	return nil
}

type statusReport struct {
	service string
	paths   paths
	jobs    []cron.Job
	jobsErr error
	history *cron.State
	now     time.Time
}

func writeStatus(w io.Writer, r statusReport) {
	fprintf(w, "service  %s\n", r.service)
	fprintf(w, "crontab  %s\n", r.paths.crontab)
	fprintf(w, "log      %s\n", r.paths.log)
	fprintf(w, "state    %s\n", r.paths.state)
	if r.jobsErr != nil {
		fprintf(w, "\ncrontab error: %v\n", r.jobsErr)
		return
	}
	fprintf(w, "\n%d jobs\n", len(r.jobs))
	for _, job := range r.jobs {
		fprintf(w, "\nline %d  %s  %s\n", job.Line, job.Spec, job.Command)
		user := job.User
		if user == "" {
			user = "SYSTEM"
		}
		fprintf(w, "        user     %s\n", user)
		if opts := job.Options(); opts != "" {
			fprintf(w, "        options  %s\n", opts)
		}
		fprintf(w, "        next     %s\n", nextRun(job, r.now))
		js, _ := r.history.Lookup(job)
		fprintf(w, "        last     %s\n", lastRun(js))
	}
}

func nextRun(job cron.Job, now time.Time) string {
	if job.Reboot {
		return "when the service starts"
	}
	next, ok := job.Schedule.Next(now)
	if !ok {
		return "never"
	}
	return next.Format(statusTimeFormat)
}

func lastRun(js cron.JobState) string {
	if js.LastStart.IsZero() {
		return "never run"
	}
	// A finish older than the start belongs to the previous run.
	if js.LastFinish.Before(js.LastStart) {
		return js.LastStart.Format(statusTimeFormat) + ", running or no result recorded"
	}
	return fmt.Sprintf("%s, %s (took %s)", js.LastStart.Format(statusTimeFormat), js.LastResult, js.LastFinish.Sub(js.LastStart).Round(time.Second))
}
