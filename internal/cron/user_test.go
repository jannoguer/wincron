package cron

import (
	"os"
	"reflect"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func encodeEnvBlock(t *testing.T, envs []string) *uint16 {
	t.Helper()
	var block []uint16
	for _, env := range envs {
		block = append(block, utf16.Encode([]rune(env))...)
		block = append(block, 0)
	}
	block = append(block, 0)
	return &block[0]
}

func TestParseEnvBlock(t *testing.T) {
	want := []string{"Path=C:\\bin;C:\\tools", "USERPROFILE=C:\\Users\\jan", "UNICODE=wörld"}
	got := parseEnvBlock(encodeEnvBlock(t, want))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseEnvBlock = %v, want %v", got, want)
	}
}

func TestParseEnvBlockEmpty(t *testing.T) {
	if got := parseEnvBlock(encodeEnvBlock(t, nil)); got != nil {
		t.Errorf("parseEnvBlock of empty block = %v, want nil", got)
	}
}

func TestEnvValue(t *testing.T) {
	envs := []string{"Path=C:\\bin", "USERPROFILE=C:\\Users\\jan", "EMPTY="}
	tests := []struct {
		name, want string
	}{
		{"USERPROFILE", "C:\\Users\\jan"},
		{"userprofile", "C:\\Users\\jan"},
		{"PATH", "C:\\bin"},
		{"EMPTY", ""},
		{"MISSING", ""},
	}
	for _, tt := range tests {
		if got := envValue(envs, tt.name); got != tt.want {
			t.Errorf("envValue(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestTokenBelongsTo(t *testing.T) {
	token := windows.GetCurrentProcessToken()
	if tokenUser, err := token.GetTokenUser(); err == nil && tokenUser.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		// Under SYSTEM (e.g. via psexec -s), %USERNAME% reports the
		// machine account (COMPUTERNAME$), not what LookupAccount
		// resolves for the SYSTEM SID ("SYSTEM"/"NT AUTHORITY"). Real
		// jobs only ever match tokenBelongsTo against real user session
		// tokens, never the service's own SYSTEM token, so this is a
		// test-environment mismatch, not a tokenBelongsTo bug.
		t.Skip("USERNAME does not match LookupAccount for the SYSTEM account")
	}
	name := os.Getenv("USERNAME")
	domain := os.Getenv("USERDOMAIN")
	if name == "" {
		t.Skip("USERNAME not set")
	}
	if !tokenBelongsTo(token, name) {
		t.Errorf("tokenBelongsTo(%q) = false, want true", name)
	}
	if domain != "" && !tokenBelongsTo(token, domain+`\`+name) {
		t.Errorf("tokenBelongsTo(%q) = false, want true", domain+`\`+name)
	}
	if tokenBelongsTo(token, "no-such-user-wincron") {
		t.Error("tokenBelongsTo matched a nonexistent user")
	}
}

func TestEnvironForToken(t *testing.T) {
	envs, err := environForToken(windows.GetCurrentProcessToken())
	if err != nil {
		t.Fatal(err)
	}
	if envValue(envs, "SystemRoot") == "" {
		t.Errorf("environment %d entries, missing SystemRoot", len(envs))
	}
}

func TestCutFold(t *testing.T) {
	tests := []struct {
		s, prefix, want string
		ok              bool
	}{
		{"user=jan", "user=", "jan", true},
		{"USER=jan", "user=", "jan", true},
		{"user=", "user=", "", true},
		{"use=jan", "user=", "", false},
		{"", "user=", "", false},
	}
	for _, tt := range tests {
		got, ok := cutFold(tt.s, tt.prefix)
		if got != tt.want || ok != tt.ok {
			t.Errorf("cutFold(%q, %q) = (%q, %v), want (%q, %v)", tt.s, tt.prefix, got, ok, tt.want, tt.ok)
		}
	}
}
