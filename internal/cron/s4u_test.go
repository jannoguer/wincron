package cron

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestFirstGrantedLogonFallsBackToNetwork(t *testing.T) {
	var tried []uint32
	token, err := firstGrantedLogon(func(logonType uint32) (windows.Token, error) {
		tried = append(tried, logonType)
		if logonType == logonTypeBatch {
			return 0, fmt.Errorf("LsaLogonUser: %w", windows.STATUS_LOGON_TYPE_NOT_GRANTED)
		}
		return 7, nil
	})
	if err != nil || token != 7 {
		t.Fatalf("firstGrantedLogon = (%d, %v), want token 7", token, err)
	}
	if len(tried) != 2 || tried[0] != logonTypeBatch || tried[1] != logonTypeNetwork {
		t.Errorf("tried logon types %v, want batch then network", tried)
	}
}

func TestFirstGrantedLogonStopsOnOtherErrors(t *testing.T) {
	calls := 0
	boom := errors.New("boom")
	_, err := firstGrantedLogon(func(uint32) (windows.Token, error) {
		calls++
		return 0, boom
	})
	if !errors.Is(err, boom) || calls != 1 {
		t.Errorf("err = %v after %d calls, want boom after 1", err, calls)
	}
	calls = 0
	_, err = firstGrantedLogon(func(uint32) (windows.Token, error) {
		calls++
		return 0, windows.STATUS_LOGON_TYPE_NOT_GRANTED
	})
	if !errors.Is(err, windows.STATUS_LOGON_TYPE_NOT_GRANTED) || calls != len(s4uLogonTypes) {
		t.Errorf("err = %v after %d calls, want the refusal after every logon type", err, calls)
	}
}

func TestS4ULogonInfoLayout(t *testing.T) {
	// MSV1_0_S4U_LOGON and KERB_S4U_LOGON: two ULONGs, two UNICODE_STRINGs.
	if got, want := unsafe.Sizeof(s4uLogonInfo{}), 8+2*unsafe.Sizeof(windows.NTUnicodeString{}); got != want {
		t.Errorf("s4uLogonInfo is %d bytes, want %d", got, want)
	}
	if got, want := unsafe.Offsetof(s4uLogonInfo{}.Account), uintptr(8); got != want {
		t.Errorf("Account at offset %d, want %d", got, want)
	}
}

func TestResolveAccountPicksPackage(t *testing.T) {
	name, domain, computer := os.Getenv("USERNAME"), os.Getenv("USERDOMAIN"), os.Getenv("COMPUTERNAME")
	if name == "" || domain == "" || computer == "" {
		t.Skip("USERNAME, USERDOMAIN or COMPUTERNAME not set")
	}
	account, err := resolveAccount(name)
	if err != nil {
		t.Fatal(err)
	}
	local := strings.EqualFold(domain, computer)
	if local != (account.pkg == msv1Package) {
		t.Errorf("account %+v for a user in %q on %q: package does not match local=%v", account, domain, computer, local)
	}
}

func TestResolveAccountRejectsUnknownUser(t *testing.T) {
	if _, err := resolveAccount("no-such-user-wincron"); err == nil {
		t.Error("resolveAccount accepted an unknown user")
	}
}

func TestUserContextUnknownUserFailsClearly(t *testing.T) {
	// Without SeTcb no session token is found, so this reaches the S4U path
	// and stops at account resolution; nothing is logged on.
	if _, err := userContext("no-such-user-wincron"); err == nil || !strings.Contains(err.Error(), "unknown user") {
		t.Errorf("err = %v, want an unknown user error", err)
	}
}

// Needs SYSTEM and a local account named by WINCRON_TEST_S4U_USER, whose
// password is irrelevant: S4U logs it on without one.
func TestS4USessionEndToEnd(t *testing.T) {
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !self.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		t.Skip("requires LocalSystem (run with psexec -s)")
	}
	user := os.Getenv("WINCRON_TEST_S4U_USER")
	if user == "" {
		t.Skip("set WINCRON_TEST_S4U_USER to a local account to test the background logon")
	}
	session, err := s4uSession(user)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := session.close(); err != nil {
			t.Errorf("releasing the session: %v", err)
		}
	}()
	spec := procSpec{
		appName: `C:\Windows\System32\cmd.exe`,
		cmdLine: `"C:\Windows\System32\cmd.exe" /S /C "echo RUNAS:%USERNAME%& echo PROFILE:%USERPROFILE%"`,
		token:   session.token,
		desktop: &session.desktop,
		env:     session.env,
		dir:     envValue(session.env, "USERPROFILE"),
		flags:   windows.CREATE_NO_WINDOW,
	}
	var out cappedBuffer
	out.max = 4096
	result, err := runProcess(context.Background(), spec, 0, &out, t.Logf)
	if err != nil || result.exitCode != 0 {
		t.Fatalf("runProcess = (%+v, %v)", result, err)
	}
	if !strings.Contains(strings.ToLower(string(out.buf)), strings.ToLower("RUNAS:"+user)) {
		t.Errorf("output %q does not show the job running as %q", out.buf, user)
	}
}
