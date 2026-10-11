//go:build unix

package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// fileLock is an advisory, OS-released process lock.
//
// The OS releases a flock when the holding process exits for any reason, so a
// crashed supervisor leaves no stale lock and a restart is never blocked by one.
// That is why the lock, and not the socket file, is the authority on "exactly
// one supervisor".
type fileLock struct {
	file *os.File
}

// acquireFileLock takes the lock without blocking.
func acquireFileLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("daemon: lock %s: %w", path, err)
	}
	return &fileLock{file: file}, nil
}

// release drops the lock and closes the file.
func (l *fileLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if err != nil {
		return fmt.Errorf("daemon: unlock: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("daemon: close lock: %w", closeErr)
	}
	return nil
}

// listen binds the Unix-domain endpoint owner-only.
//
// A socket file left behind by a killed process is removed, but only after the
// caller holds the lock (so no live daemon owns it) and only when the path is
// really a socket — this never removes a regular file that merely shares the
// name.
func listen(endpoint string) (net.Listener, error) {
	if err := checkEndpointLength(endpoint); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(endpoint); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("daemon: %s exists and is not a socket; refusing to remove it", endpoint)
		}
		if err := os.Remove(endpoint); err != nil {
			return nil, fmt.Errorf("daemon: remove stale socket %s: %w", endpoint, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("daemon: inspect %s: %w", endpoint, err)
	}
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("daemon: listen on %s: %w", endpoint, err)
	}
	// Owner-only. net.Listen applies the process umask, so chmod explicitly
	// rather than hoping the umask is restrictive.
	if err := os.Chmod(endpoint, 0o600); err != nil {
		listener.Close()
		return nil, fmt.Errorf("daemon: chmod %s: %w", endpoint, err)
	}
	return listener, nil
}

// dial connects to the Unix-domain endpoint.
func dial(endpoint string) (net.Conn, error) {
	return net.Dial("unix", endpoint)
}
