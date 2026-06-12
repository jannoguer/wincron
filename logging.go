package main

import (
	"io"
	"log"
	"os"
)

func openLogger(path string, mirrorStdout bool) (*log.Logger, io.Closer, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, nil, err
	}
	var w io.Writer = f
	if mirrorStdout {
		w = io.MultiWriter(f, os.Stdout)
	}
	return log.New(w, "", log.LstdFlags), f, nil
}
