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

// paths locates wincron's files; everything but the log (see WINCRON_LOG)
// sits next to the executable.
type paths struct {
	exe, crontab, log, state string
}

func locate() (paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return paths{}, err
	}
	dir := filepath.Dir(exe)
	p := paths{
		exe:     exe,
		crontab: filepath.Join(dir, "crontab.txt"),
		log:     filepath.Join(dir, "wincron.log"),
		state:   filepath.Join(dir, "wincron-state.json"),
	}
	if v := os.Getenv("WINCRON_LOG"); v != "" {
		p.log = v
	}
	return p, nil
}

func main() {
	p, err := locate()
	if err != nil {
		fatal(err)
	}

	isService, err := svc.IsWindowsService()
	if err != nil {
		fatal(err)
	}
	if isService {
		if err := runService(p); err != nil {
			reportFatalToEventLog(err)
			fatal(err)
		}
		return
	}

	os.Exit(dispatch(commands(p), os.Args[1:], os.Stdout, os.Stderr))
}

func commands(p paths) []command {
	simple := func(name, args, summary string, f func() error) command {
		return command{name, args, summary, func(*flag.FlagSet) func([]string) error { return noArgs(name, f) }}
	}
	return []command{
		simple("list", "", "print jobs from the crontab file", func() error { return list(p.crontab) }),
		simple("edit", "", "edit a copy of the crontab, install it once it validates", func() error { return edit(p.crontab) }),
		{"load", "<file>", "validate a file and install it as the crontab", func(*flag.FlagSet) func([]string) error {
			return func(args []string) error { return load(p.crontab, args) }
		}},
		{"log", "[-n N] [-f]", "show the end of the log, like tail (-f follows)", logCommand(p)},
		simple("status", "", "show the service, the jobs, and their next and last runs", func() error { return status(p) }),
		simple("validate", "", "check the crontab file for errors", func() error { return validate(p.crontab) }),
		simple("daemon", "", "run the scheduler in the foreground (like cron -f)", func() error { return runForeground(p) }),
		simple("install", "", "install to Program Files, add to PATH, start the service", func() error { return install(p.exe) }),
		simple("uninstall", "", "remove the service, PATH entry and files (keeps crontab.txt)", func() error { return uninstall(p.exe) }),
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

func runForeground(p paths) error {
	logger, closer, err := openLogger(p.log, true)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cron.NewScheduler(p.crontab, p.state, logger).Run(ctx)
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
