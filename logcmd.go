package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/windows"
)

const followPollInterval = 500 * time.Millisecond

// logCommand registers the flags of "wincron log".
func logCommand(p paths) func(fs *flag.FlagSet) func([]string) error {
	return func(fs *flag.FlagSet) func([]string) error {
		lines := fs.Int("n", 10, "number of lines to show")
		follow := fs.Bool("f", false, "keep printing new lines as they are written")
		return func(args []string) error {
			if len(args) > 0 {
				return usageError{errors.New("log takes no arguments")}
			}
			if *lines < 0 {
				return usageError{errors.New("-n must not be negative")}
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			return showLog(ctx, p.log, os.Stdout, *lines, *follow)
		}
	}
}

// showLog prints the last n lines of the log at path and, with follow, keeps
// printing what is appended until ctx ends.
func showLog(ctx context.Context, path string, w io.Writer, n int, follow bool) error {
	f, err := openShared(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if _, err := w.Write(lastLines(data, n)); err != nil {
		return err
	}
	if !follow {
		return nil
	}
	return followFile(ctx, f, path, w, followPollInterval)
}

// openShared opens path for reading while letting the service keep writing,
// and rotate (rename) the file; Go's os.Open does not share for delete.
func openShared(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	h, err := windows.CreateFile(name, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// lastLines returns the last n lines of data, or all of it if shorter.
func lastLines(data []byte, n int) []byte {
	if n == 0 {
		return nil
	}
	end := len(data)
	if end > 0 && data[end-1] == '\n' {
		end--
	}
	for i := end - 1; i >= 0; i-- {
		if data[i] == '\n' {
			if n--; n == 0 {
				return data[i+1:]
			}
		}
	}
	return data
}

// followFile copies what is appended to f (positioned at the end of what was
// already shown) to w. When path stops being the file f has open, the log was
// rotated: the rest of the old file is drained, then the new one is read from
// its start.
func followFile(ctx context.Context, f *os.File, path string, w io.Writer, poll time.Duration) error {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		rotated, err := wasRotated(f, path)
		if err != nil {
			return err
		}
		if !rotated {
			continue
		}
		// Anything written to the old file between the last copy and the
		// rename is still readable.
		if _, err := io.Copy(w, f); err != nil {
			return err
		}
		next, err := openShared(path)
		if err != nil {
			return fmt.Errorf("following the rotated log: %w", err)
		}
		_ = f.Close()
		f = next
	}
}

// wasRotated reports whether path no longer names the file f has open, or
// the file shrank. A path that is briefly missing mid-rotation is not yet a
// rotation.
func wasRotated(f *os.File, path string) (bool, error) {
	current, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	open, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(open, current) {
		return true, nil
	}
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return false, err
	}
	if current.Size() < pos {
		_, err = f.Seek(0, io.SeekStart)
		return false, err
	}
	return false, nil
}
