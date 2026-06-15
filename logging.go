package main

import (
	"io"
	"log"
	"os"
	"sync"
)

const maxLogBytes = 10 << 20

type rotatingWriter struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	size    int64
	maxSize int64
}

func newRotatingWriter(path string, maxSize int64) (*rotatingWriter, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &rotatingWriter{path: path, f: f, size: info.Size(), maxSize: maxSize}, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		w.rotate()
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() {
	w.f.Close()
	os.Remove(w.path + ".1")
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		if f, oerr := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); oerr == nil {
			w.f = f
		}
		return
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	w.f = f
	w.size = 0
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

func openLogger(path string, mirrorStdout bool) (*log.Logger, io.Closer, error) {
	rw, err := newRotatingWriter(path, maxLogBytes)
	if err != nil {
		return nil, nil, err
	}
	var w io.Writer = rw
	if mirrorStdout {
		w = io.MultiWriter(rw, os.Stdout)
	}
	return log.New(w, "", log.LstdFlags), rw, nil
}
