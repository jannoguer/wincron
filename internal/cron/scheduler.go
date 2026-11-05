package cron

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"sync"
	"time"
)

const jobShutdownGrace = 20 * time.Second

const maxCatchupMinutes = 60

// maxClockBack is how far the wall clock may move back and still count as a
// DST-style repeat rather than a reset.
const maxClockBack = 3 * time.Hour

// Failure describes a run that failed, timed out or could not start.
type Failure struct {
	Job    Job
	Result string
	Output []byte
}

type Scheduler struct {
	// OnFailure, if set before Run, is called after each failed run, from the
	// goroutine that ran the job. An overlap=no skip is not a failure.
	OnFailure func(Failure)
	// LogonTriggers is set by a host that calls RunLogon. Without it @logon
	// jobs never run, and the scheduler says so once.
	LogonTriggers bool

	crontabPath string
	// jobs is replaced by reloadIfChanged under jobsMu; goroutines other
	// than the reloader read it through jobList.
	jobsMu   sync.RWMutex
	jobs     []Job
	reloadMu sync.Mutex
	// jobCtx is the context scheduled jobs run under; nil unless Run is
	// between starting and shutting down. Guarded by mu.
	jobCtx      context.Context
	warnedLogon bool
	// started is closed once Run accepts RunLogon; stopping when shutdown
	// begins.
	started     chan struct{}
	stopping    chan struct{}
	stopOnce    sync.Once
	mtime       time.Time
	lastProblem string
	// repeatUntil is the wall minute the clock moved back from, while it is
	// still being replayed; zero otherwise.
	repeatUntil time.Time
	logger      *log.Logger
	state       *State
	wg          sync.WaitGroup
	mu          sync.Mutex
	running     map[string]bool
}

// NewScheduler loads the crontab and the run history kept in statePath
// (empty keeps it in memory only).
func NewScheduler(crontabPath, statePath string, logger *log.Logger) *Scheduler {
	s := &Scheduler{crontabPath: crontabPath, logger: logger, running: make(map[string]bool), started: make(chan struct{}), stopping: make(chan struct{})}
	var err error
	if s.state, err = LoadState(statePath); err != nil {
		logger.Printf("WARN state file unreadable, starting with an empty history: %v", err)
	}
	s.reloadIfChanged()
	return s
}

func (s *Scheduler) Run(ctx context.Context) {
	jobCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()

	s.setJobCtx(jobCtx)
	close(s.started)
	defer s.clearJobCtx()
	s.logger.Printf("scheduler started, %d jobs from %s", len(s.jobList()), s.crontabPath)
	s.warnIfLogonJobsCannotRun()
	s.runRebootJobs(jobCtx)
	s.runMissedJobs(jobCtx, time.Now(), nil)
	lastMinute := time.Now().Truncate(time.Minute)
	for {
		wake := time.Now().Truncate(time.Minute).Add(time.Minute)
		timer := time.NewTimer(time.Until(wake))
		select {
		case <-ctx.Done():
			timer.Stop()
			s.clearJobCtx()
			s.beginStop()
			s.shutdown(cancelJobs)
			return
		case <-timer.C:
		}
		s.reloadIfChanged()
		s.warnIfLogonJobsCannotRun()
		now := time.Now().Truncate(time.Minute)
		lastMinute = s.runDueJobs(jobCtx, lastMinute, now)
	}
}

func (s *Scheduler) runDueJobs(jobCtx context.Context, lastMinute, now time.Time) time.Time {
	due, last := s.collectDue(lastMinute, now)
	for _, job := range due {
		s.launch(jobCtx, job)
	}
	s.runMissedJobs(jobCtx, now, due)
	return last
}

// runMissedJobs starts the missed=run jobs that should have run while the
// scheduler was not looking (stopped, or asleep for longer than the catch-up
// window), except those in due, which are already starting.
func (s *Scheduler) runMissedJobs(jobCtx context.Context, now time.Time, due []Job) {
	for _, job := range s.missedJobs(now, due) {
		s.logger.Printf("MISSED %s: running it now", job.label())
		s.launch(jobCtx, job)
	}
}

// missedJobs returns the missed=run jobs whose latest scheduled time up to
// now is later than both their last start and the moment they were first
// seen, so a new job never fires on first sight. A job still running under
// overlap=no is not reported again every minute.
func (s *Scheduler) missedJobs(now time.Time, due []Job) []Job {
	var missed []Job
	for _, job := range s.jobList() {
		if !job.MissedRun || job.Reboot || slices.ContainsFunc(due, func(d Job) bool { return d.key == job.key }) {
			continue
		}
		prev, ok := job.Schedule.Prev(now)
		history, known := s.state.Lookup(job)
		if !ok || !known {
			continue
		}
		since := history.FirstSeen
		if history.LastStart.After(since) {
			since = history.LastStart
		}
		if prev.After(since) && (!job.NoOverlap || !s.isRunning(job.key)) {
			missed = append(missed, job)
		}
	}
	return missed
}

func (s *Scheduler) isRunning(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[key]
}

// collectDue returns the jobs that match at least one minute since
// lastMinute, up to now. Each job is returned at most once, so waking up
// late (system resume, clock jump) cannot launch a burst of concurrent
// copies.
//
// Minutes are walked on the wall clock, as Vixie cron does, so a job inside
// the hour a DST change skips runs once at the jump. When the wall clock
// moves back by less than maxClockBack (DST fall-back, a manual set-back),
// only wildcard jobs run until it passes the point it came from, so most
// jobs do not run twice for the same wall minute.
func (s *Scheduler) collectDue(lastMinute, now time.Time) ([]Job, time.Time) {
	if now.Equal(lastMinute) {
		return nil, lastMinute
	}
	wallNow, wallLast := wallClock(now), wallClock(lastMinute)
	first := wallLast.Add(time.Minute)
	switch {
	case !wallNow.After(wallLast):
		back := wallLast.Sub(wallNow)
		// A big jump back would stall every job until the clock caught up.
		if back >= maxClockBack {
			s.logger.Printf("clock moved back %s, resuming from %s", back, now.Format(time.RFC3339))
			s.repeatUntil = time.Time{}
			return nil, now
		}
		s.logger.Printf("clock moved back %s, running only wildcard jobs until %s", back, wallLast.Format("15:04"))
		if wallLast.After(s.repeatUntil) {
			s.repeatUntil = wallLast
		}
		first = wallNow
	case now.Sub(lastMinute.Add(time.Minute)) > maxCatchupMinutes*time.Minute:
		late := now.Sub(lastMinute.Add(time.Minute))
		skipTo := now.Add(-maxCatchupMinutes * time.Minute)
		s.logger.Printf("woke %s late, skipping catch-up to %s", late.Round(time.Minute), skipTo.Format(time.RFC3339))
		first = wallClock(skipTo)
	}
	var due []Job
	for _, job := range s.jobList() {
		if job.Reboot || job.Logon {
			continue
		}
		for minute := first; !minute.After(wallNow); minute = minute.Add(time.Minute) {
			repeated := !minute.After(s.repeatUntil)
			if job.Schedule.Matches(minute) && (!repeated || job.Schedule.isWildcard()) {
				due = append(due, job)
				break
			}
		}
	}
	if !wallNow.Before(s.repeatUntil) {
		s.repeatUntil = time.Time{}
	}
	return due, now
}

// wallClock returns t's wall-clock reading as a UTC time, which walks
// minute by minute without DST adjustments.
func wallClock(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
}

func (s *Scheduler) setJobCtx(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobCtx = ctx
}

// beginStop tells retrying jobs to give up.
func (s *Scheduler) beginStop() {
	s.stopOnce.Do(func() { close(s.stopping) })
}

func (s *Scheduler) clearJobCtx() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobCtx = nil
}

// Started returns a channel closed once Run is accepting RunLogon calls.
func (s *Scheduler) Started() <-chan struct{} {
	return s.started
}

// RunLogon starts the @logon jobs for a user who just logged on: those
// without user=, and those whose user= satisfies loggedOn. It does nothing
// while the scheduler is not running. Call it from one goroutine, the one
// that also stops the scheduler, so a launch never races shutdown.
func (s *Scheduler) RunLogon(loggedOn func(user string) bool) {
	s.mu.Lock()
	ctx := s.jobCtx
	s.mu.Unlock()
	if ctx == nil {
		return
	}
	s.reloadIfChanged()
	for _, job := range s.jobList() {
		if job.Logon && (job.User == "" || loggedOn(job.User)) {
			s.launch(ctx, job)
		}
	}
}

// warnIfLogonJobsCannotRun says once that @logon jobs need the service.
func (s *Scheduler) warnIfLogonJobsCannotRun() {
	if s.LogonTriggers || s.warnedLogon {
		return
	}
	for _, job := range s.jobList() {
		if job.Logon {
			s.warnedLogon = true
			s.logger.Print("WARN @logon jobs only run when wincron runs as the service; they are ignored here")
			return
		}
	}
}

func (s *Scheduler) runRebootJobs(jobCtx context.Context) {
	for _, job := range s.jobList() {
		if job.Reboot {
			s.launch(jobCtx, job)
		}
	}
}

func (s *Scheduler) launch(jobCtx context.Context, job Job) {
	if job.NoOverlap && !s.claim(job.key) {
		s.logger.Printf("SKIP %s: an earlier run is still going", job.label())
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if job.NoOverlap {
			defer s.release(job.key)
		}
		s.warnOnSaveError(s.state.recordStart(job, time.Now()))
		result := s.runWithRetries(jobCtx, job)
		s.warnOnSaveError(s.state.recordFinish(job, time.Now(), result.Summary))
		if !result.OK && s.OnFailure != nil {
			s.OnFailure(Failure{Job: job, Result: result.Summary, Output: result.Output})
		}
	}()
}

// runWithRetries runs job, and repeats it after job.RetryDelay while it
// fails, up to job.Retries times. Retries stop at shutdown, even though a
// running attempt gets the shutdown grace.
func (s *Scheduler) runWithRetries(jobCtx context.Context, job Job) RunResult {
	result := runJob(jobCtx, job, s.logger, nil)
	for attempt := 1; !result.OK && attempt <= job.Retries && jobCtx.Err() == nil; attempt++ {
		s.logger.Printf("RETRY %s: attempt %d of %d in %s after: %s", job.label(), attempt, job.Retries, job.RetryDelay, result.Summary)
		select {
		case <-time.After(job.RetryDelay):
		case <-s.stopping:
			return result
		case <-jobCtx.Done():
			return result
		}
		select {
		case <-s.stopping:
			return result
		default:
		}
		result = runJob(jobCtx, job, s.logger, nil)
	}
	return result
}

// claim marks a job as running and reports whether it was free to start.
func (s *Scheduler) claim(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[key] {
		return false
	}
	s.running[key] = true
	return true
}

func (s *Scheduler) release(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, key)
}

func (s *Scheduler) shutdown(cancelJobs context.CancelFunc) {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(jobShutdownGrace):
		s.logger.Print("shutdown grace expired, terminating running jobs")
		cancelJobs()
		s.wg.Wait()
	}
	s.logger.Print("scheduler stopped")
}

func (s *Scheduler) jobList() []Job {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	return s.jobs
}

// RunNow runs the job on crontab line now, as the scheduler would, after
// picking up a changed crontab. Output is also copied to stream. The run is
// logged but changes neither the run history nor overlap tracking, and it
// ends when ctx does.
func (s *Scheduler) RunNow(ctx context.Context, line int, stream io.Writer) (RunResult, error) {
	s.reloadIfChanged()
	for _, job := range s.jobList() {
		if job.Line == line {
			return runJob(ctx, job, s.logger, stream), nil
		}
	}
	return RunResult{}, fmt.Errorf("no job on line %d of %s", line, s.crontabPath)
}

func (s *Scheduler) reloadIfChanged() {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	info, err := os.Stat(s.crontabPath)
	if err != nil {
		if !s.mtime.IsZero() {
			s.logProblem("crontab stat failed, keeping %d jobs: %v", len(s.jobs), err)
		} else {
			s.logProblem("crontab unavailable, running with no jobs: %v", err)
		}
		return
	}
	if info.ModTime().Equal(s.mtime) {
		s.lastProblem = ""
		return
	}
	jobs, err := LoadFile(s.crontabPath)
	if err != nil {
		s.logProblem("crontab reload failed, keeping %d jobs: %v", len(s.jobs), err)
		return
	}
	s.jobsMu.Lock()
	s.jobs = jobs
	s.jobsMu.Unlock()
	if s.state.sync(jobs, time.Now()) {
		s.warnOnSaveError(s.state.save())
	}
	if !s.mtime.IsZero() || s.lastProblem != "" {
		s.logger.Printf("crontab reloaded, %d jobs", len(jobs))
	}
	s.lastProblem = ""
	s.mtime = info.ModTime()
}

// logProblem drops a message identical to the previous problem, which the
// once-a-minute reload would otherwise repeat for as long as it lasts.
func (s *Scheduler) logProblem(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if msg == s.lastProblem {
		return
	}
	s.lastProblem = msg
	s.logger.Print(msg)
}

func (s *Scheduler) warnOnSaveError(err error) {
	if err != nil {
		s.logger.Printf("WARN cannot save the state file: %v", err)
	}
}
