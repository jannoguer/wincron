package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

func testCommands(ran *[]string) []command {
	return []command{
		{"alpha", "", "does alpha", func(*flag.FlagSet) func([]string) error {
			return noArgs("alpha", func() error { *ran = append(*ran, "alpha"); return nil })
		}},
		{"beta", "<n>", "does beta", func(fs *flag.FlagSet) func([]string) error {
			verbose := fs.Bool("v", false, "be chatty")
			return func(args []string) error {
				*ran = append(*ran, "beta")
				if *verbose {
					*ran = append(*ran, "verbose")
				}
				return nil
			}
		}},
		{"fail", "", "always fails", func(*flag.FlagSet) func([]string) error {
			return noArgs("fail", func() error { return errors.New("boom") })
		}},
	}
}

func runCLI(args ...string) (code int, stdout, stderr string, ran []string) {
	var out, errOut bytes.Buffer
	code = dispatch(testCommands(&ran), args, &out, &errOut)
	return code, out.String(), errOut.String(), ran
}

func TestDispatchHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		code, stdout, _, _ := runCLI(args...)
		if code != 0 || !strings.Contains(stdout, "alpha") || !strings.Contains(stdout, "does beta") {
			t.Errorf("%v: code %d, stdout %q, want 0 and the command list", args, code, stdout)
		}
	}
}

func TestDispatchHelpForCommand(t *testing.T) {
	for _, args := range [][]string{{"help", "beta"}, {"beta", "-h"}, {"beta", "--help"}} {
		code, stdout, _, ran := runCLI(args...)
		if code != 0 || len(ran) != 0 {
			t.Errorf("%v: code %d, ran %v, want 0 without running the command", args, code, ran)
		}
		for _, want := range []string{"usage: wincron beta <n>", "does beta", "-v"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%v: stdout %q does not contain %q", args, stdout, want)
			}
		}
	}
}

func TestDispatchUnknownCommandExits2(t *testing.T) {
	for _, args := range [][]string{{"nope"}, {"help", "nope"}, {}} {
		if code, _, stderr, _ := runCLI(args...); code != 2 || stderr == "" {
			t.Errorf("%v: code %d, stderr %q, want 2 and a message", args, code, stderr)
		}
	}
}

func TestDispatchRunsCommandWithFlags(t *testing.T) {
	code, _, _, ran := runCLI("beta", "-v", "x")
	if code != 0 || strings.Join(ran, ",") != "beta,verbose" {
		t.Errorf("code %d, ran %v, want 0 and beta,verbose", code, ran)
	}
}

func TestDispatchBadFlagAndExtraArgsExit2(t *testing.T) {
	if code, _, stderr, _ := runCLI("beta", "-z"); code != 2 || !strings.Contains(stderr, "usage: wincron beta") {
		t.Errorf("bad flag: code %d, stderr %q, want 2 with usage", code, stderr)
	}
	if code, _, _, ran := runCLI("alpha", "extra"); code != 2 || len(ran) != 0 {
		t.Errorf("extra arg: code %d, ran %v, want 2 without running", code, ran)
	}
}

func TestDispatchFailureExits1(t *testing.T) {
	if code, _, stderr, _ := runCLI("fail"); code != 1 || !strings.Contains(stderr, "boom") {
		t.Errorf("code %d, stderr %q, want 1 and the error", code, stderr)
	}
}
