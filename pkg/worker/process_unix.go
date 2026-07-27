//go:build unix

package worker

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the worker in a process group of its own, led by the
// worker itself.
//
// killProcessGroup's negative pid is only safe because of this: without
// Setpgid the worker stays in the orchestrator's group and the same call would
// signal the server. The two belong together.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup SIGKILLs the group led by pid: the worker plus anything it
// spawned and did not reap, which is how a soffice child dies with the job
// that started it.
//
// A group that is already gone reports os.ErrProcessDone, the answer os/exec
// wants from a Cmd.Cancel that raced the process exiting.
func killProcessGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// signalOf reports the signal that killed the process, or 0 when it exited on
// its own.
func signalOf(state *os.ProcessState) syscall.Signal {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0
	}
	return status.Signal()
}
