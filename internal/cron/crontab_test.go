package cron

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeCrontab(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crontab.txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFileBasic(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "0 5 * * 1 backup.exe --full\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	job := jobs[0]
	if job.Command != "backup.exe --full" {
		t.Errorf("Command = %q, want %q", job.Command, "backup.exe --full")
	}
	if job.Line != 1 || job.Reboot || job.Envs != nil {
		t.Errorf("unexpected job fields: %+v", job)
	}
	if !job.Schedule.dayOfWeekRestricted {
		t.Error("day-of-week should be restricted")
	}
}

func TestLoadFileSkipsCommentsAndBlanks(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "# comment\n\n   \n  # indented comment\n0 5 * * 1 backup.exe\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if jobs[0].Line != 5 {
		t.Errorf("Line = %d, want 5 (comments and blanks must still count)", jobs[0].Line)
	}
}

func TestLoadFileEmpty(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("got %d jobs, want 0", len(jobs))
	}
}

func TestLoadFileMissing(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "missing.txt")); err == nil {
		t.Fatal("expected error for missing file, got none")
	}
}

func TestLoadFileEnvAppliesToFollowingJobs(t *testing.T) {
	content := "FOO=1\n* * * * * first.exe\nBAR=two words\n* * * * * second.exe\n"
	jobs, err := LoadFile(writeCrontab(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}
	if want := []string{"FOO=1"}; !reflect.DeepEqual(jobs[0].Envs, want) {
		t.Errorf("first job Envs = %v, want %v", jobs[0].Envs, want)
	}
	if want := []string{"FOO=1", "BAR=two words"}; !reflect.DeepEqual(jobs[1].Envs, want) {
		t.Errorf("second job Envs = %v, want %v", jobs[1].Envs, want)
	}
}

func TestLoadFileEnvOnly(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "FOO=1\nBAR=2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("got %d jobs, want 0", len(jobs))
	}
}

func TestLoadFileReboot(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "@reboot  echo  spaced   args\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || !jobs[0].Reboot {
		t.Fatalf("expected one reboot job, got %+v", jobs)
	}
	if want := "echo  spaced   args"; jobs[0].Command != want {
		t.Errorf("Command = %q, want %q (inner spacing must be preserved)", jobs[0].Command, want)
	}
}

func TestLoadFileUserField(t *testing.T) {
	tests := []struct {
		name, content, wantUser, wantCommand string
	}{
		{"schedule job", "0 5 * * 1 user=jan backup.exe --full\n", "jan", "backup.exe --full"},
		{"domain user", "* * * * * user=CORP\\jan sync.exe\n", "CORP\\jan", "sync.exe"},
		{"uppercase key", "* * * * * USER=jan sync.exe\n", "jan", "sync.exe"},
		{"reboot job", "@reboot user=jan agent.exe\n", "jan", "agent.exe"},
		{"no user field", "* * * * * echo user=less\n", "", "echo user=less"},
		{"double-quoted user", "* * * * * user=\"Jan Noguer\" powershell.exe -File test.ps1\n", "Jan Noguer", "powershell.exe -File test.ps1"},
		{"single-quoted user", "* * * * * user='Jan Noguer' powershell.exe -File test.ps1\n", "Jan Noguer", "powershell.exe -File test.ps1"},
		{"quoted reboot", "@reboot user=\"Jan Noguer\" agent.exe\n", "Jan Noguer", "agent.exe"},
		{"quoted domain", "* * * * * user=\"CORP\\Jan Noguer\" sync.exe\n", "CORP\\Jan Noguer", "sync.exe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs, err := LoadFile(writeCrontab(t, tt.content))
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 1 {
				t.Fatalf("got %d jobs, want 1", len(jobs))
			}
			if jobs[0].User != tt.wantUser {
				t.Errorf("User = %q, want %q", jobs[0].User, tt.wantUser)
			}
			if jobs[0].Command != tt.wantCommand {
				t.Errorf("Command = %q, want %q", jobs[0].Command, tt.wantCommand)
			}
		})
	}
}

func TestLoadFileErrors(t *testing.T) {
	tests := []struct {
		name, content, wantErr string
	}{
		{"reboot without command", "@reboot\n", "@reboot requires a command"},
		{"unsupported nickname", "@daily foo.exe\n", "unsupported nickname"},
		{"too few fields", "* * * * *\n", "expected 5 schedule fields and a command"},
		{"single word", "1FOO=bar\n", "expected 5 schedule fields and a command"},
		{"bad minute", "61 * * * * foo.exe\n", "minute"},
		{"bad day of week", "* * * * 9 foo.exe\n", "day of week"},
		{"empty user", "* * * * * user= foo.exe\n", "user= requires a name"},
		{"user without command", "* * * * * user=jan\n", "expected a command after user=jan"},
		{"reboot user without command", "@reboot user=jan\n", "@reboot requires a command"},
		{"unterminated double quote", `* * * * * user="Jan Noguer foo.exe` + "\n", "unterminated"},
		{"unterminated single quote", "* * * * * user='Jan Noguer foo.exe\n", "unterminated"},
		{"empty quoted user", `* * * * * user="" foo.exe` + "\n", "user= requires a name"},
		{"empty single-quoted user", "* * * * * user='' foo.exe\n", "user= requires a name"},
		{"text after quoted user", `* * * * * user="jan"foo.exe` + "\n", "unexpected text after quoted user="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFile(writeCrontab(t, "# padding line\n"+tt.content))
			if err == nil {
				t.Fatal("expected error, got none")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
			if !strings.Contains(err.Error(), "line 2") {
				t.Errorf("error %q does not report line 2", err)
			}
		})
	}
}

func TestLoadFileTabSeparated(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "0\t5\t*\t*\t1\tC:\\tools\\job.exe /x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(jobs))
	}
	if want := "C:\\tools\\job.exe /x"; jobs[0].Command != want {
		t.Errorf("Command = %q, want %q", jobs[0].Command, want)
	}
}

func TestLoadFileUnicodeCommand(t *testing.T) {
	jobs, err := LoadFile(writeCrontab(t, "* * * * * echo héllo wörld\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "echo héllo wörld"; jobs[0].Command != want {
		t.Errorf("Command = %q, want %q", jobs[0].Command, want)
	}
}

func TestParseEnv(t *testing.T) {
	tests := []struct {
		line string
		want string
		ok   bool
	}{
		{"FOO=bar", "FOO=bar", true},
		{"FOO = bar ", "FOO=bar", true},
		{"FOO=", "FOO=", true},
		{"FOO=a=b", "FOO=a=b", true},
		{"_private=1", "_private=1", true},
		{"Path2=C:\\bin", "Path2=C:\\bin", true},
		{"lower_case=x", "lower_case=x", true},
		{"1FOO=bar", "", false},
		{"FOO BAR=1", "", false},
		{"=bar", "", false},
		{"no assignment here", "", false},
		{"* * * * * set FOO=bar", "", false},
	}
	for _, tt := range tests {
		got, ok := parseEnv(tt.line)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseEnv(%q) = (%q, %v), want (%q, %v)", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}

func TestIsEnvName(t *testing.T) {
	valid := []string{"A", "_", "FOO", "foo", "F1", "_9", "Path_2"}
	invalid := []string{"", "1A", "A-B", "A B", "A.B", "ÑAME", "A=B"}
	for _, s := range valid {
		if !isEnvName(s) {
			t.Errorf("isEnvName(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if isEnvName(s) {
			t.Errorf("isEnvName(%q) = true, want false", s)
		}
	}
}

func TestSkipFields(t *testing.T) {
	tests := []struct {
		line   string
		fields int
		want   string
	}{
		{"* * * * * echo hi", 5, "echo hi"},
		{"*/5 9-17 1,15 * 1-5 run.exe  --flag \"a b\"", 5, "run.exe  --flag \"a b\""},
		{"@reboot foo.exe", 1, "foo.exe"},
		{"0\t5\t*\t*\t1\tcmd.exe", 5, "cmd.exe"},
	}
	for _, tt := range tests {
		off := skipFields(tt.line, tt.fields)
		if got := tt.line[off:]; got != tt.want {
			t.Errorf("skipFields(%q, %d) -> %q, want %q", tt.line, tt.fields, got, tt.want)
		}
	}
}

func TestSnapshotIsIndependent(t *testing.T) {
	envs := []string{"A=1"}
	snap := snapshot(envs)
	envs[0] = "A=2"
	envs = append(envs, "B=1")
	_ = envs
	if snap[0] != "A=1" || len(snap) != 1 {
		t.Errorf("snapshot changed with source slice: %v", snap)
	}
	if snapshot(nil) != nil {
		t.Error("snapshot(nil) should be nil")
	}
}
