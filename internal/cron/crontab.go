package cron

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const scheduleFieldCount = 5

// scheduleNicknames maps nicknames to 5-field schedules; @reboot has no
// equivalent and is handled separately.
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
	Schedule  Schedule
	Command   string
	Line      int
	Reboot    bool
	Envs      []string
	User      string
	Timeout   time.Duration
	NoOverlap bool
}

func LoadFile(path string) ([]Job, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

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
				opts, command, err := jobTail(line, 1, lineNo)
				if err != nil {
					return nil, err
				}
				if command == "" {
					return nil, fmt.Errorf("line %d: @reboot requires a command", lineNo)
				}
				job := opts.job(command, lineNo, envs)
				job.Reboot = true
				jobs = append(jobs, job)
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

// buildScheduledJob parses the optional job options and command after the
// first skipFields fields; label names the schedule token in the no-command
// error.
func buildScheduledJob(line string, lineNo int, schedule Schedule, skipFields int, label string, envs []string) (Job, error) {
	opts, command, err := jobTail(line, skipFields, lineNo)
	if err != nil {
		return Job{}, err
	}
	if command == "" {
		switch {
		case opts.User != "":
			return Job{}, fmt.Errorf("line %d: expected a command after user=%s", lineNo, opts.User)
		case opts.Timeout > 0 || opts.NoOverlap:
			return Job{}, fmt.Errorf("line %d: expected a command after the job options", lineNo)
		default:
			return Job{}, fmt.Errorf("line %d: %s requires a command", lineNo, label)
		}
	}
	job := opts.job(command, lineNo, envs)
	job.Schedule = schedule
	return job, nil
}

// jobOptions holds the key=value tokens accepted between the schedule and
// the command.
type jobOptions struct {
	User      string
	Timeout   time.Duration
	NoOverlap bool
}

var optionKeys = []string{"user=", "timeout=", "overlap="}

func (o jobOptions) job(command string, lineNo int, envs []string) Job {
	return Job{
		Command:   command,
		Line:      lineNo,
		Envs:      snapshot(envs),
		User:      o.User,
		Timeout:   o.Timeout,
		NoOverlap: o.NoOverlap,
	}
}

func (o *jobOptions) set(key, value string, lineNo int) error {
	switch key {
	case "user=":
		o.User = value
	case "timeout=":
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return fmt.Errorf("line %d: timeout= requires a positive duration such as 30s or 5m", lineNo)
		}
		o.Timeout = d
	case "overlap=":
		switch strings.ToLower(value) {
		case "yes":
			o.NoOverlap = false
		case "no":
			o.NoOverlap = true
		default:
			return fmt.Errorf("line %d: overlap= requires yes or no", lineNo)
		}
	}
	return nil
}

// jobTail parses the optional key=value options and the command after the
// first n fields. command is empty when nothing follows.
func jobTail(line string, fields, lineNo int) (jobOptions, string, error) {
	opts, cmdOff, err := parseOptions(line, skipFields(line, fields), lineNo)
	if err != nil {
		return jobOptions{}, "", err
	}
	return opts, line[cmdOff:], nil
}

// parseOptions reads the leading key=value tokens at pos and returns the
// offset where the command starts.
func parseOptions(line string, pos, lineNo int) (jobOptions, int, error) {
	var opts jobOptions
	seen := make(map[string]bool, len(optionKeys))
	for pos < len(line) {
		key, ok := matchOptionKey(line[pos:])
		if !ok {
			break
		}
		if seen[key] {
			return jobOptions{}, 0, fmt.Errorf("line %d: %s given twice", lineNo, key)
		}
		seen[key] = true
		value, next, err := parseOptionValue(line, pos, key, lineNo)
		if err != nil {
			return jobOptions{}, 0, err
		}
		if err := opts.set(key, value, lineNo); err != nil {
			return jobOptions{}, 0, err
		}
		pos = next
	}
	return opts, pos, nil
}

func matchOptionKey(s string) (string, bool) {
	for _, key := range optionKeys {
		if _, ok := cutFold(s, key); ok {
			return key, true
		}
	}
	return "", false
}

// parseOptionValue reads the value of the key= token at pos, quoted or not,
// and returns the offset of whatever follows it.
func parseOptionValue(line string, pos int, key string, lineNo int) (string, int, error) {
	valueStart := pos + len(key)
	rest := line[valueStart:]
	if rest == "" {
		return "", 0, missingValue(key, lineNo)
	}
	if rest[0] == '"' || rest[0] == '\'' {
		quote := rest[0]
		closeIdx := strings.IndexByte(rest[1:], quote)
		if closeIdx < 0 {
			return "", 0, fmt.Errorf("line %d: unterminated %c in %s value", lineNo, quote, key)
		}
		value := rest[1 : 1+closeIdx]
		if value == "" {
			return "", 0, missingValue(key, lineNo)
		}
		afterQuote := valueStart + 1 + closeIdx + 1
		if afterQuote < len(line) {
			if r, _ := utf8.DecodeRuneInString(line[afterQuote:]); !unicode.IsSpace(r) {
				return "", 0, fmt.Errorf("line %d: unexpected text after quoted %s value", lineNo, key)
			}
		}
		return value, skipSpace(line, afterQuote), nil
	}
	end := strings.IndexFunc(rest, unicode.IsSpace)
	if end < 0 {
		return rest, len(line), nil
	}
	if end == 0 {
		return "", 0, missingValue(key, lineNo)
	}
	return rest[:end], skipSpace(line, valueStart+end), nil
}

func missingValue(key string, lineNo int) error {
	if key == "user=" {
		return fmt.Errorf("line %d: user= requires a name", lineNo)
	}
	return fmt.Errorf("line %d: %s requires a value", lineNo, key)
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
