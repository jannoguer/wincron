package cron

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// JobState is what the scheduler remembers about one job between runs.
type JobState struct {
	FirstSeen  time.Time `json:"first_seen"`
	LastStart  time.Time `json:"last_start,omitzero"`
	LastFinish time.Time `json:"last_finish,omitzero"`
	LastResult string    `json:"last_result,omitempty"`
}

// State persists per-job run history, keyed by Job.key. A state with an
// empty path lives in memory only.
type State struct {
	mu   sync.Mutex
	path string
	jobs map[string]JobState
}

type stateFile struct {
	Jobs map[string]JobState `json:"jobs"`
}

// LoadState reads the state file at path. A missing file is an empty state;
// an unreadable one is an error, so the caller can say so and start fresh.
func LoadState(path string) (*State, error) {
	st := &State{path: path, jobs: make(map[string]JobState)}
	if path == "" {
		return st, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	var f stateFile
	if err := json.Unmarshal(data, &f); err != nil {
		return st, fmt.Errorf("parsing %s: %w", path, err)
	}
	if f.Jobs != nil {
		st.jobs = f.Jobs
	}
	return st, nil
}

// Lookup returns the recorded state of job.
func (st *State) Lookup(job Job) (JobState, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	js, ok := st.jobs[job.key]
	return js, ok
}

// sync registers jobs not seen before (first seen at now) and drops state
// for jobs no longer in the crontab. It reports whether anything changed.
func (st *State) sync(jobs []Job, now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	live := make(map[string]bool, len(jobs))
	changed := false
	for _, job := range jobs {
		live[job.key] = true
		if _, ok := st.jobs[job.key]; !ok {
			st.jobs[job.key] = JobState{FirstSeen: now}
			changed = true
		}
	}
	for key := range st.jobs {
		if !live[key] {
			delete(st.jobs, key)
			changed = true
		}
	}
	return changed
}

func (st *State) recordStart(job Job, at time.Time) error {
	return st.update(job, func(js *JobState) { js.LastStart = at })
}

func (st *State) recordFinish(job Job, at time.Time, result string) error {
	return st.update(job, func(js *JobState) {
		js.LastFinish = at
		js.LastResult = result
	})
}

func (st *State) update(job Job, f func(*JobState)) error {
	st.mu.Lock()
	js := st.jobs[job.key]
	if js.FirstSeen.IsZero() {
		js.FirstSeen = time.Now()
	}
	f(&js)
	st.jobs[job.key] = js
	st.mu.Unlock()
	return st.save()
}

// save writes the file atomically: a temporary file in the same directory,
// then a rename over the old one.
func (st *State) save() error {
	if st.path == "" {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	data, err := json.MarshalIndent(stateFile{Jobs: st.jobs}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(st.path), ".wincron-state-*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), st.path)
	}
	if err != nil {
		return errors.Join(err, os.Remove(tmp.Name()))
	}
	return nil
}
