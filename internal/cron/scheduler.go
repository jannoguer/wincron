package cron

import (
	"context"
	"log"
	"os"
	"sync"
	"time"
)

const jobShutdownGrace = 20 * time.Second

const maxCatchupMinutes = 60

type Scheduler struct {
	crontabPath string
	jobs        []Job
	mtime       time.Time
	logger      *log.Logger
	wg          sync.WaitGroup
}

func NewScheduler(crontabPath string, logger *log.Logger) *Scheduler {
	s := &Scheduler{crontabPath: crontabPath, logger: logger}
	s.reloadIfChanged()
	return s
}

func (s *Scheduler) Run(ctx context.Context) {
	jobCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()

	s.logger.Printf("scheduler started, %d jobs from %s", len(s.jobs), s.crontabPath)
	s.runRebootJobs(jobCtx)
	lastMinute := time.Now().Truncate(time.Minute)
	for {
		wake := time.Now().Truncate(time.Minute).Add(time.Minute)
		timer := time.NewTimer(time.Until(wake))
		select {
		case <-ctx.Done():
			timer.Stop()
			s.shutdown(cancelJobs)
			return
		case <-timer.C:
		}
		s.reloadIfChanged()
		now := time.Now().Truncate(time.Minute)
		lastMinute = s.runDueJobs(jobCtx, lastMinute, now)
	}
}

func (s *Scheduler) runDueJobs(jobCtx context.Context, lastMinute, now time.Time) time.Time {
	if !now.After(lastMinute) {
		return lastMinute
	}
	minute := lastMinute.Add(time.Minute)
	if now.Sub(minute) > maxCatchupMinutes*time.Minute {
		skipTo := now.Add(-maxCatchupMinutes * time.Minute)
		s.logger.Printf("woke %s late, skipping catch-up to %s", now.Sub(minute).Round(time.Minute), skipTo.Format(time.RFC3339))
		minute = skipTo
	}
	for ; !minute.After(now); minute = minute.Add(time.Minute) {
		for _, job := range s.jobs {
			if job.Reboot {
				continue
			}
			if job.Schedule.Matches(minute) {
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					runJob(jobCtx, job, s.logger)
				}()
			}
		}
	}
	return now
}

func (s *Scheduler) runRebootJobs(jobCtx context.Context) {
	for _, job := range s.jobs {
		if !job.Reboot {
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			runJob(jobCtx, job, s.logger)
		}()
	}
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

func (s *Scheduler) reloadIfChanged() {
	info, err := os.Stat(s.crontabPath)
	if err != nil {
		if !s.mtime.IsZero() {
			s.logger.Printf("crontab stat failed, keeping %d jobs: %v", len(s.jobs), err)
		} else {
			s.logger.Printf("crontab unavailable, running with no jobs: %v", err)
		}
		return
	}
	if info.ModTime().Equal(s.mtime) {
		return
	}
	jobs, err := LoadFile(s.crontabPath)
	if err != nil {
		s.logger.Printf("crontab reload failed, keeping %d jobs: %v", len(s.jobs), err)
		return
	}
	s.jobs = jobs
	if !s.mtime.IsZero() {
		s.logger.Printf("crontab reloaded, %d jobs", len(jobs))
	}
	s.mtime = info.ModTime()
}
