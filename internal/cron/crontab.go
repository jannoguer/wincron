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

// scheduleNicknames maps a nickname to its equivalent 5-field schedule.
// @reboot has no schedule equivalent and is handled separately.
var scheduleNicknames = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

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
			nickname := strings.ToLower(fields[0])
			if nickname == "@reboot" {
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
			scheduleSpec, ok := scheduleNicknames[nickname]
			if !ok {
				return nil, fmt.Errorf("line %d: unsupported nickname %q", lineNo, fields[0])
			}
			schedule, err := ParseSchedule(strings.Fields(scheduleSpec))
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNo, err)
			}
			job, err := buildScheduledJob(line, lineNo, schedule, 1, fields[0], envs)
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, job)
			continue
		}
		if len(fields) < scheduleFieldCount+1 {
			return nil, fmt.Errorf("line %d: expected %d schedule fields and a command", lineNo, scheduleFieldCount)
		}
		schedule, err := ParseSchedule(fields[:scheduleFieldCount])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		job, err := buildScheduledJob(line, lineNo, schedule, scheduleFieldCount, "", envs)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// buildScheduledJob parses the optional user= field and command starting
// after skipFields fields. label names the schedule token (a nickname, or
// "" for the 5-field form, where the length check before the call already
// rules out an empty command with no user= field) and is only used for the
// no-command error when there is no user= field to name instead.
func buildScheduledJob(line string, lineNo int, schedule Schedule, skipFields int, label string, envs []string) (Job, error) {
	user, command, err := jobTail(line, skipFields, lineNo)
	if err != nil {
		return Job{}, err
	}
	if command == "" {
		if user != "" {
			return Job{}, fmt.Errorf("line %d: expected a command after user=%s", lineNo, user)
		}
		return Job{}, fmt.Errorf("line %d: %s requires a command", lineNo, label)
	}
	return Job{Schedule: schedule, User: user, Command: command, Line: lineNo, Envs: snapshot(envs)}, nil
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
