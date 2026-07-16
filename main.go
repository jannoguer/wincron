package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"

	"golang.org/x/sys/windows/svc"

	"wincron/internal/cron"
)

// version is set via -ldflags "-X main.version=..." for tagged releases.
// Local builds fall back to the VCS revision Go embeds automatically.
var version = "dev"

const usageText = `usage: wincron <command>

commands:
  list       print jobs from the crontab file
  validate   check the crontab file for errors
  run        run the scheduler in the foreground
  install    install the Windows service
  uninstall  remove the Windows service
  start      start the service
  stop       stop the service
  version    print the wincron version

  -h, --help show this help message`

func main() {
	exePath, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	exeDir := filepath.Dir(exePath)
	crontabPath := filepath.Join(exeDir, "crontab.txt")
	logPath := filepath.Join(exeDir, "wincron.log")

	isService, err := svc.IsWindowsService()
	if err != nil {
		fatal(err)
	}
	if isService {
		if err := runService(crontabPath, logPath); err != nil {
			reportFatalToEventLog(err)
			fatal(err)
		}
		return
	}

	var command string
	if len(os.Args) > 1 {
		command = os.Args[1]
	}

	switch command {
	case "-h", "--help":
		fmt.Println(usageText)
	case "list":
		err = list(crontabPath)
	case "validate":
		err = validate(crontabPath)
	case "run":
		err = runForeground(crontabPath, logPath)
	case "install":
		err = installService(exePath)
	case "uninstall":
		err = uninstallService()
	case "start":
		err = startService()
	case "stop":
		err = stopService()
	case "version":
		fmt.Println(printableVersion())
	default:
		fmt.Fprintln(os.Stderr, usageText)
		os.Exit(2)
	}
	if err != nil {
		fatal(err)
	}
}

func list(crontabPath string) error {
	data, err := os.ReadFile(crontabPath)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func validate(crontabPath string) error {
	jobs, err := cron.LoadFile(crontabPath)
	if err != nil {
		return err
	}
	fmt.Printf("OK: %d jobs\n", len(jobs))
	return nil
}

func runForeground(crontabPath, logPath string) error {
	logger, closer, err := openLogger(logPath, true)
	if err != nil {
		return err
	}
	defer closer.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cron.NewScheduler(crontabPath, logger).Run(ctx)
	return nil
}

// printableVersion returns the release-stamped version, or, for a local
// "dev" build, the VCS revision Go embeds automatically when building
// inside a git checkout.
func printableVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	for _, s := range info.Settings {
		if s.Key != "vcs.revision" {
			continue
		}
		rev := s.Value
		if len(rev) > 12 {
			rev = rev[:12]
		}
		if dirty := buildSetting(info, "vcs.modified"); dirty == "true" {
			rev += "-dirty"
		}
		return version + "+" + rev
	}
	return version
}

func buildSetting(info *debug.BuildInfo, key string) string {
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "wincron:", err)
	os.Exit(1)
}
