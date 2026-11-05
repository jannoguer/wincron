package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"

	"golang.org/x/sys/windows/svc"

	"wincron/internal/cron"
)

// version is stamped via -ldflags "-X main.version=..." in release builds.
var version = "dev"

func main() {
	exePath, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	exeDir := filepath.Dir(exePath)
	crontabPath := filepath.Join(exeDir, "crontab.txt")
	logPath := filepath.Join(exeDir, "wincron.log")
	if p := os.Getenv("WINCRON_LOG"); p != "" {
		logPath = p
	}

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

	os.Exit(dispatch(commands(exePath, crontabPath, logPath), os.Args[1:], os.Stdout, os.Stderr))
}

func commands(exePath, crontabPath, logPath string) []command {
	simple := func(name, args, summary string, f func() error) command {
		return command{name, args, summary, func(*flag.FlagSet) func([]string) error { return noArgs(name, f) }}
	}
	return []command{
		simple("list", "", "print jobs from the crontab file", func() error { return list(crontabPath) }),
		simple("edit", "", "edit a copy of the crontab, install it once it validates", func() error { return edit(crontabPath) }),
		{"load", "<file>", "validate a file and install it as the crontab", func(*flag.FlagSet) func([]string) error {
			return func(args []string) error { return load(crontabPath, args) }
		}},
		simple("validate", "", "check the crontab file for errors", func() error { return validate(crontabPath) }),
		simple("run", "", "run the scheduler in the foreground", func() error { return runForeground(crontabPath, logPath) }),
		simple("install", "", "install to Program Files, add to PATH, start the service", func() error { return install(exePath) }),
		simple("uninstall", "", "remove the service, PATH entry and files (keeps crontab.txt)", func() error { return uninstall(exePath) }),
		simple("start", "", "start the service", startService),
		simple("stop", "", "stop the service", stopService),
		simple("version", "", "print the wincron version", func() error { fmt.Println(printableVersion()); return nil }),
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
	defer func() { _ = closer.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cron.NewScheduler(crontabPath, logger).Run(ctx)
	return nil
}

// printableVersion returns the stamped version, or "dev" plus the VCS
// revision Go embeds when building inside a git checkout.
func printableVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	rev := buildSetting(info, "vcs.revision")
	if rev == "" {
		return version
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if buildSetting(info, "vcs.modified") == "true" {
		rev += "-dirty"
	}
	return version + "+" + rev
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
