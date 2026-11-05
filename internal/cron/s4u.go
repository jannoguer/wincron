package cron

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// S4U (Service-for-User) logon gives the scheduler a token for a user who is
// not logged on, without their password, the way Task Scheduler runs "whether
// the user is logged on or not" tasks. The token carries no network
// credentials.

var (
	secur32 = windows.NewLazySystemDLL("secur32.dll")
	advapi  = windows.NewLazySystemDLL("advapi32.dll")
	userenv = windows.NewLazySystemDLL("userenv.dll")

	procLsaRegisterLogonProcess        = secur32.NewProc("LsaRegisterLogonProcess")
	procLsaDeregisterLogonProcess      = secur32.NewProc("LsaDeregisterLogonProcess")
	procLsaLookupAuthenticationPackage = secur32.NewProc("LsaLookupAuthenticationPackage")
	procLsaLogonUser                   = secur32.NewProc("LsaLogonUser")
	procLsaFreeReturnBuffer            = secur32.NewProc("LsaFreeReturnBuffer")
	procAllocateLocallyUniqueID        = advapi.NewProc("AllocateLocallyUniqueId")
	procLoadUserProfile                = userenv.NewProc("LoadUserProfileW")
	procUnloadUserProfile              = userenv.NewProc("UnloadUserProfile")
)

const (
	// SECURITY_LOGON_TYPE values.
	logonTypeNetwork = 3
	logonTypeBatch   = 4

	// MsV1_0S4ULogon and KerbS4ULogon share this value.
	s4uLogonMessage = 12

	msv1Package     = "MICROSOFT_AUTHENTICATION_PACKAGE_V1_0"
	kerberosPackage = "Kerberos"

	profileNoUI = 0x1 // PI_NOUI
)

// s4uLogonTypes are tried in order. Batch needs the "log on as a batch job"
// right, which ordinary users often lack; Network is granted to Users by
// default but may yield an impersonation token (see primaryToken).
var s4uLogonTypes = []uint32{logonTypeBatch, logonTypeNetwork}

// lsaString is LSA_STRING: a counted ANSI string.
type lsaString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *byte
}

func newLsaString(s string) (lsaString, []byte) {
	buf := append([]byte(s), 0)
	return lsaString{Length: uint16(len(s)), MaximumLength: uint16(len(buf)), Buffer: &buf[0]}, buf
}

// s4uLogonInfo is MSV1_0_S4U_LOGON and KERB_S4U_LOGON, which have one layout.
type s4uLogonInfo struct {
	MessageType uint32
	Flags       uint32
	Account     windows.NTUnicodeString // UserPrincipalName / ClientUpn
	Realm       windows.NTUnicodeString // DomainName / ClientRealm
}

// tokenSource is TOKEN_SOURCE.
type tokenSource struct {
	SourceName       [8]byte
	SourceIdentifier windows.LUID
}

// quotaLimits is QUOTA_LIMITS.
type quotaLimits struct {
	PagedPoolLimit        uintptr
	NonPagedPoolLimit     uintptr
	MinimumWorkingSetSize uintptr
	MaximumWorkingSetSize uintptr
	PagefileLimit         uintptr
	TimeLimit             int64
}

// s4uAccount is who to log on and through which authentication package.
type s4uAccount struct {
	name, realm string
	pkg         string
}

// resolveAccount looks user up and picks the authentication package: MSV1_0
// for local accounts, Kerberos for domain accounts.
func resolveAccount(user string) (s4uAccount, error) {
	_, domain, accType, err := windows.LookupSID("", user)
	if err != nil {
		return s4uAccount{}, fmt.Errorf("unknown user %q: %w", user, err)
	}
	if accType != windows.SidTypeUser {
		return s4uAccount{}, fmt.Errorf("%q is not a user account", user)
	}
	name := user
	if i := strings.LastIndexByte(user, '\\'); i >= 0 {
		name = user[i+1:]
	}
	computer, err := windows.ComputerName()
	if err != nil {
		return s4uAccount{}, fmt.Errorf("reading the computer name: %w", err)
	}
	if strings.EqualFold(domain, computer) {
		return s4uAccount{name: name, realm: computer, pkg: msv1Package}, nil
	}
	if upn, ok := translateToUPN(domain + `\` + name); ok {
		return s4uAccount{name: upn, pkg: kerberosPackage}, nil
	}
	return s4uAccount{name: name, realm: domain, pkg: kerberosPackage}, nil
}

// translateToUPN converts DOMAIN\name to name@dns.domain.
func translateToUPN(samName string) (string, bool) {
	in, err := windows.UTF16PtrFromString(samName)
	if err != nil {
		return "", false
	}
	buf := make([]uint16, 256)
	n := uint32(len(buf))
	if err := windows.TranslateName(in, windows.NameSamCompatible, windows.NameUserPrincipal, &buf[0], &n); err != nil {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

// s4uToken returns a primary token for user without a logon session.
func s4uToken(user string) (windows.Token, error) {
	account, err := resolveAccount(user)
	if err != nil {
		return 0, err
	}
	return firstGrantedLogon(func(logonType uint32) (windows.Token, error) {
		return lsaS4U(account, logonType)
	})
}

// firstGrantedLogon tries each logon type until one is not refused for lack of
// the logon right.
func firstGrantedLogon(logon func(logonType uint32) (windows.Token, error)) (windows.Token, error) {
	var err error
	for _, logonType := range s4uLogonTypes {
		var token windows.Token
		if token, err = logon(logonType); err == nil {
			return token, nil
		}
		if !errors.Is(err, windows.STATUS_LOGON_TYPE_NOT_GRANTED) {
			break
		}
	}
	return 0, err
}

func lsaS4U(account s4uAccount, logonType uint32) (windows.Token, error) {
	if err := enablePrivilege("SeTcbPrivilege"); err != nil {
		return 0, err
	}
	name, nameBuf := newLsaString("wincron")
	var lsa windows.Handle
	var mode uint32
	if status, _, _ := procLsaRegisterLogonProcess.Call(uintptr(unsafe.Pointer(&name)), uintptr(unsafe.Pointer(&lsa)), uintptr(unsafe.Pointer(&mode))); status != 0 {
		return 0, fmt.Errorf("LsaRegisterLogonProcess: %w", windows.NTStatus(status))
	}
	defer func() { _, _, _ = procLsaDeregisterLogonProcess.Call(uintptr(lsa)) }()

	pkgName, pkgBuf := newLsaString(account.pkg)
	var pkg uint32
	if status, _, _ := procLsaLookupAuthenticationPackage.Call(uintptr(lsa), uintptr(unsafe.Pointer(&pkgName)), uintptr(unsafe.Pointer(&pkg))); status != 0 {
		return 0, fmt.Errorf("LsaLookupAuthenticationPackage %s: %w", account.pkg, windows.NTStatus(status))
	}

	info := s4uLogonInfo{MessageType: s4uLogonMessage}
	accountStr, err := windows.NewNTUnicodeString(account.name)
	if err != nil {
		return 0, err
	}
	info.Account = *accountStr
	if account.realm != "" {
		realmStr, err := windows.NewNTUnicodeString(account.realm)
		if err != nil {
			return 0, err
		}
		info.Realm = *realmStr
	}

	source := tokenSource{}
	copy(source.SourceName[:], "wincron")
	if r, _, err := procAllocateLocallyUniqueID.Call(uintptr(unsafe.Pointer(&source.SourceIdentifier))); r == 0 {
		return 0, fmt.Errorf("AllocateLocallyUniqueId: %w", err)
	}

	var (
		profile    unsafe.Pointer
		profileLen uint32
		logonID    windows.LUID
		token      windows.Handle
		quotas     quotaLimits
		subStatus  int32
	)
	status, _, _ := procLsaLogonUser.Call(
		uintptr(lsa),
		uintptr(unsafe.Pointer(&name)),
		uintptr(logonType),
		uintptr(pkg),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		0,
		uintptr(unsafe.Pointer(&source)),
		uintptr(unsafe.Pointer(&profile)),
		uintptr(unsafe.Pointer(&profileLen)),
		uintptr(unsafe.Pointer(&logonID)),
		uintptr(unsafe.Pointer(&token)),
		uintptr(unsafe.Pointer(&quotas)),
		uintptr(unsafe.Pointer(&subStatus)),
	)
	runtime.KeepAlive(nameBuf)
	runtime.KeepAlive(pkgBuf)
	runtime.KeepAlive(accountStr)
	if profile != nil {
		_, _, _ = procLsaFreeReturnBuffer.Call(uintptr(profile))
	}
	if status != 0 {
		return 0, fmt.Errorf("LsaLogonUser (logon type %d): %w", logonType, windows.NTStatus(status))
	}
	return primaryToken(windows.Token(token))
}

// primaryToken returns a primary-token copy of token and closes token. A
// network S4U logon yields an impersonation token, which CreateProcessAsUser
// rejects.
func primaryToken(token windows.Token) (windows.Token, error) {
	defer func() { _ = token.Close() }()
	var primary windows.Token
	if err := windows.DuplicateTokenEx(token, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation, windows.TokenPrimary, &primary); err != nil {
		return 0, fmt.Errorf("making a primary token: %w", err)
	}
	return primary, nil
}

// enablePrivilege enables a privilege this process holds but has disabled.
func enablePrivilege(name string) error {
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
		return fmt.Errorf("looking up %s: %w", name, err)
	}
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return fmt.Errorf("opening the process token: %w", err)
	}
	defer func() { _ = token.Close() }()
	privileges := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges:     [1]windows.LUIDAndAttributes{{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}},
	}
	if err := windows.AdjustTokenPrivileges(token, false, &privileges, 0, nil, nil); err != nil {
		return fmt.Errorf("enabling %s: %w", name, err)
	}
	return nil
}

// profileInfo is PROFILEINFOW.
type profileInfo struct {
	Size        uint32
	Flags       uint32
	UserName    *uint16
	ProfilePath *uint16
	DefaultPath *uint16
	ServerName  *uint16
	PolicyPath  *uint16
	Profile     windows.Handle
}

// loadProfile loads the user's registry hive and profile, which a logged-off
// user does not have loaded. Unload it with unloadProfile.
func loadProfile(token windows.Token, user string) (windows.Handle, error) {
	for _, name := range []string{"SeBackupPrivilege", "SeRestorePrivilege"} {
		if err := enablePrivilege(name); err != nil {
			return 0, err
		}
	}
	userName, err := windows.UTF16PtrFromString(user)
	if err != nil {
		return 0, err
	}
	info := profileInfo{Flags: profileNoUI, UserName: userName}
	info.Size = uint32(unsafe.Sizeof(info))
	if r, _, err := procLoadUserProfile.Call(uintptr(token), uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0, fmt.Errorf("LoadUserProfile: %w", err)
	}
	return info.Profile, nil
}

func unloadProfile(token windows.Token, profile windows.Handle) error {
	if r, _, err := procUnloadUserProfile.Call(uintptr(token), uintptr(profile)); r == 0 {
		return fmt.Errorf("UnloadUserProfile: %w", err)
	}
	return nil
}
