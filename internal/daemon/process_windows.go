//go:build windows

package daemon

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// detachSysProcAttr starts a spawned supervisor in its own process group,
// detached from the launcher's console, so it survives the launcher's exit.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

// executablePath is the binary to re-execute for `serve`.
func executablePath() (string, error) {
	return os.Executable()
}

// osErrProcessDone is the error a Kill returns when the target already exited.
var osErrProcessDone = os.ErrProcessDone
