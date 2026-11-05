package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"wincron/internal/cron"
)

// The service listens here so "wincron test" can run a job through it, as
// SYSTEM or the job's user, exactly like a scheduled run.
const pipeName = `\\.\pipe\wincron`

// pipeSDDL grants full access to SYSTEM and Administrators only, since a
// request runs a crontab line with the service's privileges.
const pipeSDDL = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"

const pipeBufferBytes = 64 << 10

// The protocol is line framed, in both directions. The client sends one line:
//
//	RUN <crontab line number>
//
// The server answers with any number of output frames, then one final line:
//
//	OUT <n>        followed by n raw bytes of job output
//	DONE <0|1> <summary>   the run ended; 1 means it succeeded
//	ERR <message>  the request could not be run
type pipeServer struct {
	name, sddl string
	run        func(ctx context.Context, line int, stream io.Writer) (cron.RunResult, error)
	logger     *log.Logger
	wg         sync.WaitGroup
}

// serve accepts connections until ctx ends, then waits for running requests.
func (p *pipeServer) serve(ctx context.Context) error {
	defer p.wg.Wait()
	sd, err := windows.SecurityDescriptorFromString(p.sddl)
	if err != nil {
		return fmt.Errorf("pipe security descriptor: %w", err)
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, err := windows.UTF16PtrFromString(p.name)
	if err != nil {
		return err
	}
	flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_FIRST_PIPE_INSTANCE)
	for {
		handle, err := windows.CreateNamedPipe(name, flags,
			windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
			windows.PIPE_UNLIMITED_INSTANCES, pipeBufferBytes, pipeBufferBytes, 0, sa)
		if err != nil {
			return fmt.Errorf("creating pipe %s: %w", p.name, err)
		}
		flags = windows.PIPE_ACCESS_DUPLEX
		err = connectOrCancel(ctx, handle)
		if ctx.Err() != nil {
			_ = windows.CloseHandle(handle)
			return nil
		}
		if err != nil {
			p.logger.Printf("WARN test pipe: accepting a client: %v", err)
			_ = windows.CloseHandle(handle)
			continue
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handle(ctx, handle)
		}()
	}
}

// connectOrCancel waits for a client on a listening pipe handle. When ctx
// ends it cancels the wait, retrying because the cancel is lost if it lands
// just before ConnectNamedPipe starts.
func connectOrCancel(ctx context.Context, handle windows.Handle) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		for {
			_ = windows.CancelIoEx(handle, nil)
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	err := windows.ConnectNamedPipe(handle, nil)
	if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return nil
	}
	return err
}

func (p *pipeServer) handle(ctx context.Context, handle windows.Handle) {
	f := os.NewFile(uintptr(handle), p.name)
	defer func() {
		_ = windows.FlushFileBuffers(handle)
		_ = windows.DisconnectNamedPipe(handle)
		_ = f.Close()
	}()
	// A client that never sends its request must not outlive the service.
	defer context.AfterFunc(ctx, func() { _ = windows.CancelIoEx(handle, nil) })()
	line, err := readRequest(f)
	if err != nil {
		p.logger.Printf("WARN test pipe: bad request: %v", err)
		writeFrame(f, "ERR %v\n", err)
		return
	}
	result, err := p.run(ctx, line, frameWriter{f})
	if err != nil {
		writeFrame(f, "ERR %v\n", err)
		return
	}
	ok := 0
	if result.OK {
		ok = 1
	}
	writeFrame(f, "DONE %d %s\n", ok, result.Summary)
}

// readRequest reads the single request line without buffering past it.
func readRequest(r io.Reader) (int, error) {
	var buf []byte
	one := make([]byte, 1)
	for len(buf) < 64 {
		if _, err := io.ReadFull(r, one); err != nil {
			return 0, err
		}
		if one[0] == '\n' {
			break
		}
		buf = append(buf, one[0])
	}
	arg, ok := strings.CutPrefix(strings.TrimSpace(string(buf)), "RUN ")
	if !ok {
		return 0, errors.New("expected RUN <line>")
	}
	line, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || line < 1 {
		return 0, fmt.Errorf("bad line number %q", arg)
	}
	return line, nil
}

func writeFrame(w io.Writer, format string, args ...any) {
	// The client may be gone; the run's own log has the outcome.
	_, _ = fmt.Fprintf(w, format, args...)
}

// frameWriter sends job output to the client as OUT frames.
type frameWriter struct{ w io.Writer }

func (f frameWriter) Write(p []byte) (int, error) {
	frame := append([]byte(fmt.Sprintf("OUT %d\n", len(p))), p...)
	if _, err := f.w.Write(frame); err != nil {
		return 0, err
	}
	return len(p), nil
}

// runViaService asks the service to run crontab line line, copying the job's
// output to stdout. It returns an error if the job failed.
func runViaService(name string, line int, stdout io.Writer) error {
	f, err := openPipe(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(f, "RUN %d\n", line); err != nil {
		return err
	}
	reader := bufio.NewReader(f)
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("the service closed the connection before the job finished: %w", err)
		}
		kind, rest, _ := strings.Cut(strings.TrimRight(header, "\r\n"), " ")
		switch kind {
		case "OUT":
			n, err := strconv.ParseInt(rest, 10, 64)
			if err != nil {
				return fmt.Errorf("bad output frame %q", header)
			}
			if _, err := io.CopyN(stdout, reader, n); err != nil {
				return err
			}
		case "DONE":
			ok, summary, _ := strings.Cut(rest, " ")
			fprintf(stdout, "job finished: %s\n", summary)
			if ok != "1" {
				return fmt.Errorf("job failed: %s", summary)
			}
			return nil
		case "ERR":
			return errors.New(rest)
		default:
			return fmt.Errorf("unexpected reply %q", header)
		}
	}
}

// openPipe connects to the service's pipe, waiting briefly while every
// instance is busy.
func openPipe(name string) (*os.File, error) {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h, err := windows.CreateFile(ptr, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		switch {
		case err == nil:
			return os.NewFile(uintptr(h), name), nil
		case errors.Is(err, windows.ERROR_FILE_NOT_FOUND):
			return nil, errors.New("cannot reach the wincron service: is it running? (wincron status)")
		case errors.Is(err, windows.ERROR_ACCESS_DENIED):
			return nil, errors.New("access to the wincron service was denied: run from an elevated shell")
		case errors.Is(err, windows.ERROR_PIPE_BUSY) && time.Now().Before(deadline):
			time.Sleep(50 * time.Millisecond)
		default:
			return nil, fmt.Errorf("connecting to the wincron service: %w", err)
		}
	}
}

// testCommand registers "wincron test <line>".
func testCommand(*flag.FlagSet) func([]string) error {
	return func(args []string) error {
		if len(args) != 1 {
			return usageError{errors.New("test takes one crontab line number")}
		}
		line, err := strconv.Atoi(args[0])
		if err != nil || line < 1 {
			return usageError{fmt.Errorf("%q is not a crontab line number", args[0])}
		}
		return runViaService(pipeName, line, os.Stdout)
	}
}
