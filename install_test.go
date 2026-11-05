package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddPathEntry(t *testing.T) {
	const dir = `C:\Program Files\wincron`
	tests := []struct {
		name, path, want string
		changed          bool
	}{
		{"empty", "", dir, true},
		{"append", `C:\bin`, `C:\bin;` + dir, true},
		{"trailing semicolon", `C:\bin;`, `C:\bin;` + dir, true},
		{"already present", `C:\bin;` + dir, `C:\bin;` + dir, false},
		{"present with other case and slash", `C:\bin;c:\program files\WINCRON\`, `C:\bin;c:\program files\WINCRON\`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := addPathEntry(tt.path, dir)
			if got != tt.want || changed != tt.changed {
				t.Errorf("addPathEntry = (%q, %v), want (%q, %v)", got, changed, tt.want, tt.changed)
			}
		})
	}
}

func TestRemovePathEntry(t *testing.T) {
	const dir = `C:\Program Files\wincron`
	tests := []struct {
		name, path, want string
		changed          bool
	}{
		{"only entry", dir, "", true},
		{"middle", `C:\a;` + dir + `;C:\b`, `C:\a;C:\b`, true},
		{"duplicate and case", dir + `;C:\a;` + strings.ToUpper(dir) + `\`, `C:\a`, true},
		{"absent", `C:\a;C:\b`, `C:\a;C:\b`, false},
		{"similar prefix is kept", dir + `-old`, dir + `-old`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := removePathEntry(tt.path, dir)
			if got != tt.want || changed != tt.changed {
				t.Errorf("removePathEntry = (%q, %v), want (%q, %v)", got, changed, tt.want, tt.changed)
			}
		})
	}
}

func TestCopyExe(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.exe")
	dir := t.TempDir()
	if err := os.WriteFile(src, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wincron.exe"), []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst, err := copyExe(src, dir)
	if err != nil {
		t.Fatal(err)
	}
	if dst != filepath.Join(dir, "wincron.exe") || readFile(t, dst) != "v2" {
		t.Errorf("copyExe = %q with content %q, want the upgraded wincron.exe", dst, readFile(t, dst))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("directory has %d entries, want no temporary file left", len(entries))
	}
}

func TestCopyExeOntoItselfIsANoOp(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "wincron.exe")
	if err := os.WriteFile(exe, []byte("same"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst, err := copyExe(exe, dir)
	if err != nil || dst != exe || readFile(t, exe) != "same" {
		t.Errorf("copyExe onto itself = (%q, %v), content %q, want a no-op", dst, err, readFile(t, exe))
	}
}

func TestEnsureCrontabKeepsExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crontab.txt")
	if err := ensureCrontab(path); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != crontabTemplate {
		t.Errorf("new crontab = %q, want the template", got)
	}
	if err := os.WriteFile(path, []byte("* * * * * mine.exe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCrontab(path); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); got != "* * * * * mine.exe\n" {
		t.Errorf("crontab = %q, want the user's file untouched", got)
	}
}

func TestRemoveOwnFilesKeepsCrontab(t *testing.T) {
	dir := t.TempDir()
	for _, name := range append([]string{"crontab.txt", "other.txt"}, ownFiles...) {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeOwnFiles(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, ",") != "crontab.txt,other.txt" {
		t.Errorf("left %v, want only crontab.txt and other.txt", left)
	}
}

func TestRemoveDirIfEmpty(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wincron")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "crontab.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeDirIfEmpty(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("non-empty directory was removed: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "crontab.txt")); err != nil {
		t.Fatal(err)
	}
	if err := removeDirIfEmpty(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("empty directory still exists: %v", err)
	}
}
