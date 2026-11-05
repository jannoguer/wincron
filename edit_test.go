package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditorCmdLine(t *testing.T) {
	got := editorCmdLine(`C:\Windows\System32\cmd.exe`, "code --wait", `C:\Temp\wincron 1.txt`)
	want := `"C:\Windows\System32\cmd.exe" /S /C "code --wait "C:\Temp\wincron 1.txt""`
	if got != want {
		t.Errorf("editorCmdLine = %s, want %s", got, want)
	}
}

func TestEditorPrecedence(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if got := editor(); got != "notepad" {
		t.Errorf("editor() = %q, want notepad", got)
	}
	t.Setenv("EDITOR", "ed")
	if got := editor(); got != "ed" {
		t.Errorf("editor() = %q, want ed", got)
	}
	t.Setenv("VISUAL", "vis --flag")
	if got := editor(); got != "vis --flag" {
		t.Errorf("editor() = %q, want VISUAL to win", got)
	}
}

func TestRunEditorPassesArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "my file.txt")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "cmd /c echo edited>>")
	if err := runEditor(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "edited") {
		t.Errorf("file = %q, want the editor to have appended to it", data)
	}
}

func TestReplaceFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "crontab.txt")
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(target, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "new" {
		t.Errorf("target = %q, want new", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want only the crontab (no temp file left)", len(entries))
	}
}

func TestLoadCrontabRejectsInvalidFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "crontab.txt")
	src := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(target, []byte("* * * * * good.exe\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("not a crontab line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := loadCrontab(target, src); err == nil {
		t.Fatal("loadCrontab accepted an invalid file")
	}
	if got := readFile(t, target); got != "* * * * * good.exe\n" {
		t.Errorf("target = %q, want it untouched", got)
	}
}

func TestLoadCrontabInstallsValidFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "crontab.txt")
	src := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(src, []byte("* * * * * new.exe\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := loadCrontab(target, src); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "* * * * * new.exe\n" {
		t.Errorf("target = %q, want the new content", got)
	}
}

// testEdit runs an edit session whose "editor" writes edits[i] on its i-th
// run, and whose install goes straight to loadCrontab.
func testEdit(t *testing.T, initial string, edits []string, stdin string) (target string, out string, err error) {
	t.Helper()
	target = filepath.Join(t.TempDir(), "crontab.txt")
	if err := os.WriteFile(target, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	run := 0
	s := &editSession{
		crontabPath: target,
		runEditor: func(path string) error {
			content := edits[run]
			run++
			return os.WriteFile(path, []byte(content), 0644)
		},
		install: func(tmp string) error { return loadCrontab(target, tmp) },
		in:      strings.NewReader(stdin),
		out:     &buf,
	}
	err = s.run()
	return target, buf.String(), err
}

func TestEditUnchanged(t *testing.T) {
	const initial = "* * * * * a.exe\n"
	target, out, err := testEdit(t, initial, []string{initial}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no changes made to crontab") {
		t.Errorf("output %q does not report no changes", out)
	}
	if got := readFile(t, target); got != initial {
		t.Errorf("target = %q, want unchanged", got)
	}
}

func TestEditInstallsValidCopy(t *testing.T) {
	target, _, err := testEdit(t, "* * * * * a.exe\n", []string{"* * * * * b.exe\n"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "* * * * * b.exe\n" {
		t.Errorf("target = %q, want the edited content", got)
	}
}

func TestEditRetryAfterInvalidEdit(t *testing.T) {
	target, out, err := testEdit(t, "* * * * * a.exe\n", []string{"garbage\n", "* * * * * c.exe\n"}, "y\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Do you want to retry the same edit? (y/n)") {
		t.Errorf("output %q does not offer a retry", out)
	}
	if got := readFile(t, target); got != "* * * * * c.exe\n" {
		t.Errorf("target = %q, want the corrected content", got)
	}
}

func TestEditDeclinedRetryKeepsTargetAndCopy(t *testing.T) {
	target, _, err := testEdit(t, "* * * * * a.exe\n", []string{"garbage\n"}, "n\n")
	if err == nil || !strings.Contains(err.Error(), "your edits are kept in") {
		t.Fatalf("err = %v, want the kept-copy path in the error", err)
	}
	if got := readFile(t, target); got != "* * * * * a.exe\n" {
		t.Errorf("target = %q, want untouched", got)
	}
}
