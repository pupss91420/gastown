//go:build !windows

package nudge

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
)

func pollerProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	return proc.Signal(syscall.Signal(0)) == nil
}

func pollerProcessMatches(pid int, session, token string) bool {
	if !pollerProcessAlive(pid) {
		return false
	}
	var args []string
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return false
		}
		args = strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	} else {
		data, err := exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "args=").Output()
		if err != nil {
			return false
		}
		args = strings.Fields(string(data))
	}
	// The random token binds the exact invocation, even when executable paths
	// differ across upgrades. No substring-only or PID-only match is accepted.
	n := len(args)
	return n >= 5 && args[n-4] == "nudge-poller" && args[n-3] == session && args[n-2] == "--owner-token" && args[n-1] == token
}
