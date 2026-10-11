//go:build windows

package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// pipeBufferSize is the named-pipe buffer size for both directions.
const pipeBufferSize = 1 << 16

// pipeName maps an endpoint path onto a Windows named-pipe name. The name is
// derived from the same ledger hash as the Unix socket, so the endpoint identity
// does not depend on the platform.
func pipeName(endpoint string) string {
	return `\\.\pipe\bicz-` + trimSocketSuffix(endpoint)
}

// pipeListener accepts one named-pipe instance per connection, which is the
// standard Win32 pattern: CreateNamedPipe makes an instance, ConnectNamedPipe
// waits for a client on it.
//
// Known limitation, stated rather than hidden: an in-flight Accept is blocked
// inside ConnectNamedPipe and is NOT interruptible, so Close stops the listener
// but cannot cancel a pending accept. Shutdown therefore completes when the
// process exits. This path is compile-verified for windows/amd64 and
// windows/arm64; it has not been exercised at runtime on Windows.
type pipeListener struct {
	name   string
	mu     sync.Mutex
	closed bool
}

func listen(endpoint string) (net.Listener, error) {
	return &pipeListener{name: pipeName(endpoint)}, nil
}

// Accept waits for the next client.
func (l *pipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	handle, err := windows.CreateNamedPipe(
		windows.StringToUTF16Ptr(l.name),
		windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		windows.PIPE_UNLIMITED_INSTANCES,
		pipeBufferSize, pipeBufferSize, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("daemon: create named pipe %s: %w", l.name, err)
	}
	// ERROR_PIPE_CONNECTED means a client connected before ConnectNamedPipe
	// was called, which is a success, not a failure.
	if err := windows.ConnectNamedPipe(handle, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("daemon: connect named pipe %s: %w", l.name, err)
	}
	file := os.NewFile(uintptr(handle), l.name)
	if file == nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("daemon: wrap named pipe %s", l.name)
	}
	return &pipeConn{file: file}, nil
}

// Close stops the listener. See the type comment: a blocked Accept is not
// cancelled.
func (l *pipeListener) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return nil
}

// Addr is the pipe name.
func (l *pipeListener) Addr() net.Addr { return pipeAddr(l.name) }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

// pipeConn is a net.Conn over a named-pipe handle.
//
// Deadlines are not honoured: a synchronous pipe handle cannot be polled by the
// Go runtime, so Set*Deadline is an accepted no-op rather than a lie that would
// make a caller believe a timeout is enforced. Callers must not rely on
// transport timeouts on Windows.
type pipeConn struct{ file *os.File }

func (c *pipeConn) Read(b []byte) (int, error)  { return c.file.Read(b) }
func (c *pipeConn) Write(b []byte) (int, error) { return c.file.Write(b) }
func (c *pipeConn) Close() error                { return c.file.Close() }

func (c *pipeConn) LocalAddr() net.Addr              { return pipeAddr("local") }
func (c *pipeConn) RemoteAddr() net.Addr             { return pipeAddr("remote") }
func (c *pipeConn) SetDeadline(time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return nil }

// dial connects to the named pipe, retrying briefly while the pipe server
// finishes creating its next instance.
func dial(endpoint string) (net.Conn, error) {
	name := pipeName(endpoint)
	deadline := time.Now().Add(2 * time.Second)
	for {
		handle, err := windows.CreateFile(
			windows.StringToUTF16Ptr(name),
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			file := os.NewFile(uintptr(handle), name)
			if file == nil {
				windows.CloseHandle(handle)
				return nil, fmt.Errorf("daemon: wrap named pipe %s", name)
			}
			return &pipeConn{file: file}, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("daemon: connect %s: %w", name, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// fileLock is an advisory, OS-released process lock backed by LockFileEx.
type fileLock struct {
	file       *os.File
	overlapped *windows.Overlapped
}

// acquireFileLock takes the lock without blocking.
func acquireFileLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: open lock %s: %w", path, err)
	}
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped)
	if err != nil {
		file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("daemon: lock %s: %w", path, err)
	}
	return &fileLock{file: file, overlapped: overlapped}, nil
}

// release drops the lock and closes the file.
func (l *fileLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, l.overlapped)
	closeErr := l.file.Close()
	if err != nil {
		return fmt.Errorf("daemon: unlock: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("daemon: close lock: %w", closeErr)
	}
	return nil
}
