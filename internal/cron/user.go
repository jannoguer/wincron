package cron

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// wtsCurrentServerHandle is WTS_CURRENT_SERVER_HANDLE, which x/sys/windows
// does not define.
const wtsCurrentServerHandle windows.Handle = 0

// userContext returns a primary token and profile environment for the logged-on user.
// The caller must close the token.
func userContext(user string) (windows.Token, []string, error) {
	token, err := findSessionToken(user)
	if err != nil {
		return 0, nil, err
	}
	env, err := environForToken(token)
	if err != nil {
		_ = token.Close()
		return 0, nil, fmt.Errorf("reading user environment: %w", err)
	}
	return token, env, nil
}

// findSessionToken returns the session token for the user, preferring an active session.
// Requires SE_TCB_NAME (LocalSystem).
func findSessionToken(user string) (windows.Token, error) {
	var sessions *windows.WTS_SESSION_INFO
	var count uint32
	if err := windows.WTSEnumerateSessions(wtsCurrentServerHandle, 0, 1, &sessions, &count); err != nil {
		return 0, fmt.Errorf("enumerating sessions: %w", err)
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessions)))

	var disconnected windows.Token
	for _, session := range unsafe.Slice(sessions, count) {
		if session.State != windows.WTSActive && session.State != windows.WTSDisconnected {
			continue
		}
		var token windows.Token
		if err := windows.WTSQueryUserToken(session.SessionID, &token); err != nil {
			continue
		}
		if !tokenBelongsTo(token, user) {
			_ = token.Close()
			continue
		}
		if session.State == windows.WTSActive {
			if disconnected != 0 {
				_ = disconnected.Close()
			}
			return token, nil
		}
		if disconnected == 0 {
			disconnected = token
		} else {
			_ = token.Close()
		}
	}
	if disconnected != 0 {
		return disconnected, nil
	}
	return 0, fmt.Errorf("user %q is not logged on", user)
}

// tokenBelongsTo reports whether the token's account matches user (case-insensitive).
func tokenBelongsTo(token windows.Token, user string) bool {
	tokenUser, err := token.GetTokenUser()
	if err != nil {
		return false
	}
	account, domain, _, err := tokenUser.User.Sid.LookupAccount("")
	if err != nil {
		return false
	}
	return strings.EqualFold(user, account) || strings.EqualFold(user, domain+`\`+account)
}

// environForToken returns the environment variables for the given token.
func environForToken(token windows.Token) ([]string, error) {
	var block *uint16
	if err := windows.CreateEnvironmentBlock(&block, token, false); err != nil {
		return nil, err
	}
	defer func() { _ = windows.DestroyEnvironmentBlock(block) }()
	return parseEnvBlock(block), nil
}

// parseEnvBlock decodes a double-NUL-terminated list of UTF-16 strings.
func parseEnvBlock(block *uint16) []string {
	var envs []string
	p := unsafe.Pointer(block)
	for {
		n := 0
		for *(*uint16)(unsafe.Add(p, n*2)) != 0 {
			n++
		}
		if n == 0 {
			return envs
		}
		envs = append(envs, windows.UTF16ToString(unsafe.Slice((*uint16)(p), n)))
		p = unsafe.Add(p, (n+1)*2)
	}
}

// envValue returns the value of name in a NAME=value list, or "".
func envValue(envs []string, name string) string {
	for _, env := range envs {
		if rest, ok := cutFold(env, name+"="); ok {
			return rest
		}
	}
	return ""
}
