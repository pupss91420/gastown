//go:build windows

package nudge

import (
	"fmt"
	"math"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
)

func pollerProcessAlive(pid int) bool {
	if pid <= 0 || pid > math.MaxUint32 {
		return false
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	_ = windows.CloseHandle(handle)
	return true
}

func pollerProcessMatches(pid int, session, token string) bool {
	if !pollerProcessAlive(pid) {
		return false
	}
	query := fmt.Sprintf("(Get-CimInstance Win32_Process -Filter 'ProcessId = %d').CommandLine", pid)
	data, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", query).Output()
	if err != nil {
		return false
	}
	args := strings.Fields(string(data))
	n := len(args)
	return n >= 5 && args[n-4] == "nudge-poller" && args[n-3] == session && args[n-2] == "--owner-token" && strings.Trim(args[n-1], "\"") == token
}
