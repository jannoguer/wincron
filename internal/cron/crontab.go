package cron

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

const scheduleFieldCount = 5

type Job struct {
	Schedule Schedule
	Command  string
	Line     int
	Reboot   bool
	Envs     []string
	User     string
}

func LoadFile(path string) ([]Job, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var jobs []Job
	var envs []string
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if env, ok := parseEnv(line); ok {
			envs = append(envs, env)
			continue
		}
		fields := strings.Fields(line)
		if strings.HasPrefix(fields[0], "@") {
			if fields[0] != "@reboot" {
				return nil, fmt.Errorf("line %d: unsupported nickname %q (only @reboot is supported)", lineNo, fields[0])
			}
			user, command, err := jobTail(line, 1, lineNo)
			if err != nil {
				return nil, err
			}
			if command == "" {
				return nil, fmt.Errorf("line %d: @reboot requires a command", lineNo)
			}
			jobs = append(jobs, Job{Reboot: true, User: user, Command: command, Line: lineNo, Envs: snapshot(envs)})
			continue
		}
		if len(fields) < scheduleFieldCount+1 {
			return nil, fmt.Errorf("line %d: expected %d schedule fields and a command", lineNo, scheduleFieldCount)
		}
		schedule, err := ParseSchedule(fields[:scheduleFieldCount])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		user, command, err := jobTail(line, scheduleFieldCount, lineNo)
		if err != nil {
			return nil, err
		}
		if command == "" {
			return nil, fmt.Errorf("line %d: expected a command after user=%s", lineNo, user)
		}
		jobs = append(jobs, Job{Schedule: schedule, User: user, Command: command, Line: lineNo, Envs: snapshot(envs)})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// jobTail parses the optional user= field and the command after the first
// n fields. command is empty when nothing follows.
func jobTail(line string, fields, lineNo int) (user, command string, err error) {
	user, cmdOff, err := parseUserField(line, skipFields(line, fields), lineNo)
	if err != nil {
		return "", "", err
	}
	return user, line[cmdOff:], nil
}

// parseUserField extracts an optional user=NAME token at pos.
func parseUserField(line string, pos, lineNo int) (user string, cmdOff int, err error) {
	if pos >= len(line) {
		return "", pos, nil
	}
	rest, ok := cutFold(line[pos:], "user=")
	if !ok {
		return "", pos, nil
	}
	valueStart := pos + len("user=")
	if rest == "" {
		return "", pos, fmt.Errorf("line %d: user= requires a name", lineNo)
	}
	if rest[0] == '"' || rest[0] == '\'' {
		quote := rest[0]
		closeIdx := strings.IndexByte(rest[1:], quote)
		if closeIdx < 0 {
			return "", pos, fmt.Errorf("line %d: unterminated %c in user= value", lineNo, quote)
		}
		user = rest[1 : 1+closeIdx]
		if user == "" {
			return "", pos, fmt.Errorf("line %d: user= requires a name", lineNo)
		}
		afterQuote := valueStart + 1 + closeIdx + 1
		if afterQuote < len(line) {
			if r, _ := utf8.DecodeRuneInString(line[afterQuote:]); !unicode.IsSpace(r) {
				return "", pos, fmt.Errorf("line %d: unexpected text after quoted user= value", lineNo)
			}
		}
		return user, skipSpace(line, afterQuote), nil
	}
	end := strings.IndexFunc(rest, unicode.IsSpace)
	if end < 0 {
		return rest, len(line), nil
	}
	user = rest[:end]
	if user == "" {
		return "", pos, fmt.Errorf("line %d: user= requires a name", lineNo)
	}
	return user, skipSpace(line, valueStart+end), nil
}

func cutFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

func parseEnv(line string) (string, bool) {
	name, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", false
	}
	name = strings.TrimSpace(name)
	if !isEnvName(name) {
		return "", false
	}
	return name + "=" + strings.TrimSpace(value), true
}

func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func snapshot(envs []string) []string {
	if len(envs) == 0 {
		return nil
	}
	return append([]string(nil), envs...)
}

// skipFields returns the byte offset after skipping n fields.
func skipFields(line string, n int) int {
	i := 0
	for field := 0; field < n; field++ {
		i = skipSpace(line, i)
		for i < len(line) {
			r, size := utf8.DecodeRuneInString(line[i:])
			if unicode.IsSpace(r) {
				break
			}
			i += size
		}
	}
	return skipSpace(line, i)
}

func skipSpace(line string, i int) int {
	for i < len(line) {
		r, size := utf8.DecodeRuneInString(line[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += size
	}
	return i
}
