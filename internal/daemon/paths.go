package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrAlreadyRunning is returned when another live process holds the supervisor
// lock for this ledger. It is platform-neutral: both the Unix flock and the
// Windows LockFileEx path report it.
var ErrAlreadyRunning = errors.New("daemon: another supervisor already holds the lock for this ledger")

// RuntimeDir is the per-user runtime directory the supervisor's endpoint lives
// in. It is resolved in this order:
//
//  1. $BICZ_RUNTIME_DIR — explicit override, used by tests and by operators who
//     need to pin the location.
//  2. $XDG_RUNTIME_DIR — the freedesktop convention on Linux.
//  3. $TMPDIR — the per-user temporary directory macOS provides (a different
//     user's $TMPDIR is a different directory).
//  4. os.TempDir() — last resort.
//
// The directory is created mode 0700 and the endpoint inside it is owner-only,
// so another user cannot connect. That is a courtesy boundary, not a security
// boundary: a process running as the same user can connect, and nothing here
// claims otherwise (docs/CONTRACTS.md §8).
func RuntimeDir() (string, error) {
	for _, key := range []string{"BICZ_RUNTIME_DIR", "XDG_RUNTIME_DIR", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			return filepath.Join(value, "bicz"), nil
		}
	}
	return filepath.Join(os.TempDir(), "bicz"), nil
}

// EndpointHash is the short, stable digest of a ledger path that names the
// endpoint. Deriving it from the ledger path (not from a fixed name) means two
// ledgers get two independent supervisors, and the same ledger always resolves
// to the same endpoint.
func EndpointHash(ledgerPath string) (string, error) {
	absolute, err := filepath.Abs(ledgerPath)
	if err != nil {
		return "", fmt.Errorf("daemon: resolve %s: %w", ledgerPath, err)
	}
	sum := sha256.Sum256([]byte(absolute))
	return hex.EncodeToString(sum[:4]), nil
}

// DefaultSocketPath is the deterministic endpoint for a ledger.
func DefaultSocketPath(ledgerPath string) (string, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	hash, err := EndpointHash(ledgerPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "supervisor-"+hash+".sock"), nil
}

// DefaultLockPath is the process-lock path guarding a ledger's supervisor. The
// lock, not the socket, is what makes "exactly one daemon" true: the OS releases
// it when the holding process dies, so a crashed daemon never blocks a restart.
func DefaultLockPath(ledgerPath string) (string, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	hash, err := EndpointHash(ledgerPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "supervisor-"+hash+".lock"), nil
}

// launchLockPath derives the launcher lock from the supervisor's owner lock.
//
// The two are deliberately different files. The OWNER lock is what makes
// "exactly one supervisor" true: it is held by the running supervisor for its
// whole life. The LAUNCH lock is held only while a launcher decides whether to
// spawn, so launchers serialize with each other without blocking the supervisor
// they are starting (a launcher holding the owner lock would deadlock the very
// process it spawns).
func launchLockPath(lockPath string) string {
	return lockPath + ".launch"
}

// PrepareRuntimeDir creates the runtime directory owner-only.
func PrepareRuntimeDir() (string, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("daemon: create runtime dir %s: %w", dir, err)
	}
	return dir, nil
}

// socketPathLimit guards against the kernel's sun_path limit. A Unix socket path
// that is too long fails at bind time with an opaque error; this turns it into a
// clear one.
const socketPathLimit = 100

func checkEndpointLength(endpoint string) error {
	if len(endpoint) > socketPathLimit {
		return fmt.Errorf("daemon: endpoint path is %d bytes, over the %d-byte socket limit: %s (set BICZ_RUNTIME_DIR to a shorter path)",
			len(endpoint), socketPathLimit, endpoint)
	}
	return nil
}

// trimSocketSuffix is used by the Windows named-pipe name derivation.
func trimSocketSuffix(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".sock")
}
