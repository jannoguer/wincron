package main

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"wincron/internal/cron"
)

const serviceName = "wincron"

type cronService struct {
	crontabPath, logPath string
}

func (s *cronService) Execute(args []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	logger, closer, err := openLogger(s.logPath, false)
	if err != nil {
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
			status <- svc.Status{State: svc.StopPending}
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
	return service.Delete()
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
	deadline := time.Now().Add(30 * time.Second)
	for state.State != svc.Stopped {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for service to stop")
		}
		time.Sleep(300 * time.Millisecond)
		state, err = service.Query()
		if err != nil {
			return err
		}
	}
	return nil
}
