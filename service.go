package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"wincron/internal/cron"
)

const serviceName = "wincron"

type cronService struct {
	crontabPath, logPath string
}

// startStopWaitHintMillis bounds how long SCM waits before assuming a
// pending start or stop has hung; it should comfortably exceed
// jobShutdownGrace so a graceful stop never triggers that assumption.
const startStopWaitHintMillis = 30000

// eventIDFatal is the event ID used for fatal errors reported to the
// Windows event log. Stderr is not visible for a service, so this is the
// only trace of a failure that happens before (or instead of) logging to
// wincron.log.
const eventIDFatal = 1

// reportFatalToEventLog writes err to the event log under serviceName.
// Best-effort: if the event source was never registered (e.g. the service
// was installed by an older wincron, or Open itself fails) this is a no-op.
func reportFatalToEventLog(err error) {
	elog, oerr := eventlog.Open(serviceName)
	if oerr != nil {
		return
	}
	defer elog.Close()
	_ = elog.Error(eventIDFatal, err.Error())
}

func (s *cronService) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending, WaitHint: startStopWaitHintMillis}
	logger, closer, err := openLogger(s.logPath, false)
	if err != nil {
		reportFatalToEventLog(fmt.Errorf("opening log file %s: %w", s.logPath, err))
		return false, 1
	}
	defer closer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cron.NewScheduler(s.crontabPath, logger).Run(ctx)
		close(done)
	}()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for req := range requests {
		switch req.Cmd {
		case svc.Interrogate:
			status <- req.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending, WaitHint: startStopWaitHintMillis}
			cancel()
			<-done
			return false, 0
		}
	}
	cancel()
	<-done
	return false, 0
}

func runService(crontabPath, logPath string) error {
	return svc.Run(serviceName, &cronService{crontabPath: crontabPath, logPath: logPath})
}

func connectManager() (*mgr.Mgr, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("cannot connect to service manager (run as administrator): %w", err)
	}
	return m, nil
}

// registerEventSource makes serviceName usable as an event log source, so
// reportFatalToEventLog can find it. Already being registered (e.g. a
// reinstall that skipped uninstall) is not an error.
func registerEventSource() error {
	err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("registering event source: %w", err)
	}
	return nil
}

// recoveryResetPeriodSeconds is how long the service must run without
// failing before SCM resets the failure count back to the first recovery
// action.
const recoveryResetPeriodSeconds = 24 * 60 * 60

// setRecoveryActions makes SCM restart the service on failure, with
// increasing delay between attempts. openLogger failing reports Stopped
// with a nonzero exit code rather than crashing, so non-crash failures
// must be opted into separately for that path to trigger a restart too.
func setRecoveryActions(service *mgr.Service) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	if err := service.SetRecoveryActions(actions, recoveryResetPeriodSeconds); err != nil {
		return fmt.Errorf("setting recovery actions: %w", err)
	}
	if err := service.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("enabling recovery for non-crash failures: %w", err)
	}
	return nil
}

func installService(exePath string) error {
	m, err := connectManager()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	service, err := m.CreateService(serviceName, exePath, mgr.Config{
		DisplayName: "wincron scheduler",
		StartType:   mgr.StartAutomatic,
	})
	if err != nil {
		return err
	}
	if err := registerEventSource(); err != nil {
		_ = service.Close()
		return err
	}
	if err := setRecoveryActions(service); err != nil {
		_ = service.Close()
		return err
	}
	return service.Close()
}

func uninstallService() error {
	m, err := connectManager()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	service, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer service.Close()
	if err := service.Delete(); err != nil {
		return err
	}
	_ = eventlog.Remove(serviceName)
	return nil
}

func startService() error {
	m, err := connectManager()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	service, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer service.Close()
	return service.Start()
}

func stopService() error {
	m, err := connectManager()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	service, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer service.Close()
	state, err := service.Control(svc.Stop)
	if err != nil {
		return err
	}
	if state.State == svc.Stopped {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for service to stop")
		case <-ticker.C:
			state, err = service.Query()
			if err != nil {
				return err
			}
			if state.State == svc.Stopped {
				return nil
			}
		}
	}
}
