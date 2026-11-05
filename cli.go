package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// command is one wincron subcommand. setup registers its flags on fs and
// returns the function that runs it with the remaining arguments.
type command struct {
	name    string
	args    string // positional-argument synopsis, "" if none
	summary string
	setup   func(fs *flag.FlagSet) func(args []string) error
}

// noArgs adapts a function that takes no arguments.
func noArgs(name string, f func() error) func(args []string) error {
	return func(args []string) error {
		if len(args) > 0 {
			return usageError{fmt.Errorf("%s takes no arguments", name)}
		}
		return f()
	}
}

// usageError marks a mistake in how a command was invoked (exit 2).
type usageError struct{ error }

func fprintf(w io.Writer, format string, args ...any) {
	// A failed console write has no recovery.
	_, _ = fmt.Fprintf(w, format, args...)
}

func usageText(cmds []command) string {
	var b strings.Builder
	b.WriteString("usage: wincron <command> [-h]\n\ncommands:\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "  %-10s %s\n", c.name, c.summary)
	}
	b.WriteString("  help       show help, or help for one command\n")
	b.WriteString("\nRun 'wincron <command> -h' for the details of one command.")
	return b.String()
}

func commandUsage(c command, fs *flag.FlagSet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage: wincron %s", c.name)
	if c.args != "" {
		b.WriteString(" " + c.args)
	}
	fmt.Fprintf(&b, "\n\n%s\n", c.summary)
	var flags strings.Builder
	fs.SetOutput(&flags)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	if flags.Len() > 0 {
		b.WriteString("\nflags:\n" + flags.String())
	}
	return strings.TrimRight(b.String(), "\n")
}

func find(cmds []command, name string) (command, bool) {
	for _, c := range cmds {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// dispatch runs the command named by args[0] and returns the process exit
// code: 0 on success and for help, 2 for a usage mistake, 1 for a failure.
func dispatch(cmds []command, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fprintf(stderr, "%s\n", usageText(cmds))
		return 2
	}
	name, rest := args[0], args[1:]
	switch name {
	case "-h", "--help", "-help":
		fprintf(stdout, "%s\n", usageText(cmds))
		return 0
	case "help":
		return help(cmds, rest, stdout, stderr)
	}
	c, ok := find(cmds, name)
	if !ok {
		fprintf(stderr, "wincron: unknown command %q\n\n%s\n", name, usageText(cmds))
		return 2
	}
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	run := c.setup(fs)
	switch err := fs.Parse(rest); {
	case errors.Is(err, flag.ErrHelp):
		fprintf(stdout, "%s\n", commandUsage(c, fs))
		return 0
	case err != nil:
		fprintf(stderr, "wincron %s: %v\n\n%s\n", c.name, err, commandUsage(c, fs))
		return 2
	}
	err := run(fs.Args())
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fprintf(stderr, "wincron %s: %v\n\n%s\n", c.name, err, commandUsage(c, fs))
		return 2
	default:
		fprintf(stderr, "wincron: %v\n", err)
		return 1
	}
}

func help(cmds []command, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fprintf(stdout, "%s\n", usageText(cmds))
		return 0
	}
	if len(args) > 1 {
		fprintf(stderr, "usage: wincron help [command]\n")
		return 2
	}
	// "help <cmd>" is "<cmd> -h".
	return dispatch(cmds, []string{args[0], "-h"}, stdout, stderr)
}
