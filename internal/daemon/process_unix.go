//go:build unix

package daemon

import (
	"os"
	"syscall"
)

// detachSysProcAttr puts a spawned supervisor in its own session so it survives
// the launcher's exit and is not killed by the launcher's terminal.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// executablePath is the binary to re-execute for `serve`.
func executablePath() (string, error) {
	return os.Executable()
}

// osErrProcessDone is the error a Kill returns when the target already exited.
var osErrProcessDone = os.ErrProcessDone
