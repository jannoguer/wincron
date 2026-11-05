package cron

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// wtsCurrentServerHandle is WTS_CURRENT_SERVER_HANDLE, which x/sys/windows
// does not define.
const wtsCurrentServerHandle windows.Handle = 0

// errNotLoggedOn means the user has no session on this machine.
var errNotLoggedOn = errors.New("not logged on")

// userSession is what a user= job needs to start a process as that user.
type userSession struct {
	token windows.Token
	env   []string
	// desktop is lpDesktop: the user's visible desktop for a logged-on user,
	// empty (a window station of its own) for an S4U logon.
	desktop string
	// release undoes what setting the session up did.
	release func() error
}

func (s userSession) close() error {
	return s.release()
}

// userContext returns a primary token, profile environment and desktop for
// user: from their session if they are logged on, otherwise from an S4U
// logon, which has no network credentials. The caller must close the result.
func userContext(user string) (userSession, error) {
	token, err := findSessionToken(user)
	if errors.Is(err, errNotLoggedOn) {
		return s4uSession(user)
	}
	if err != nil {
		return userSession{}, err
	}
	env, err := environForToken(token)
	if err != nil {
		_ = token.Close()
		return userSession{}, fmt.Errorf("reading user environment: %w", err)
	}
	return userSession{token: token, env: env, desktop: interactiveDesktop, release: token.Close}, nil
}

// s4uSession logs user on without a session and loads their profile.
func s4uSession(user string) (userSession, error) {
	token, err := s4uToken(user)
	if err != nil {
		return userSession{}, fmt.Errorf("user %q is not logged on and a background logon failed: %w", user, err)
	}
	profile, err := loadProfile(token, user)
	if err != nil {
		_ = token.Close()
		return userSession{}, fmt.Errorf("loading the profile of %q: %w", user, err)
	}
	env, err := environForToken(token)
	if err != nil {
		return userSession{}, errors.Join(fmt.Errorf("reading user environment: %w", err), unloadProfile(token, profile), token.Close())
	}
	return userSession{token: token, env: env, release: func() error {
		return errors.Join(unloadProfile(token, profile), token.Close())
	}}, nil
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
	return 0, fmt.Errorf("user %q is %w", user, errNotLoggedOn)
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

// SessionUser returns a function reporting whether a user= name belongs to
// the user logged on to session. Call release when done with it. It needs
// SE_TCB_NAME (LocalSystem).
func SessionUser(session uint32) (matches func(user string) bool, release func(), err error) {
	var token windows.Token
	if err := windows.WTSQueryUserToken(session, &token); err != nil {
		return nil, nil, fmt.Errorf("querying the user of session %d: %w", session, err)
	}
	return func(user string) bool { return tokenBelongsTo(token, user) }, func() { _ = token.Close() }, nil
}
