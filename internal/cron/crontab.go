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
}

func LoadFile(path string) ([]Job, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var jobs []Job
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if strings.HasPrefix(fields[0], "@") {
			if fields[0] != "@reboot" {
				return nil, fmt.Errorf("line %d: unsupported nickname %q (only @reboot is supported)", lineNo, fields[0])
			}
			if len(fields) < 2 {
				return nil, fmt.Errorf("line %d: @reboot requires a command", lineNo)
			}
			jobs = append(jobs, Job{Reboot: true, Command: commandText(line, 1), Line: lineNo})
			continue
		}
		if len(fields) < scheduleFieldCount+1 {
			return nil, fmt.Errorf("line %d: expected %d schedule fields and a command", lineNo, scheduleFieldCount)
		}
		schedule, err := ParseSchedule(fields[:scheduleFieldCount])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		jobs = append(jobs, Job{Schedule: schedule, Command: commandText(line, scheduleFieldCount), Line: lineNo})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func commandText(line string, fieldCount int) string {
	i := 0
	for field := 0; field < fieldCount; field++ {
		i = skipSpace(line, i)
		for i < len(line) {
			r, size := utf8.DecodeRuneInString(line[i:])
			if unicode.IsSpace(r) {
				break
			}
			i += size
		}
	}
	return line[skipSpace(line, i):]
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
