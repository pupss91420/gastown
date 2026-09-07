package nudge

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/steveyegge/gastown/internal/util"
)

func TestPollerPidFile(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-bear"

	pidFile := pollerPidFile(townRoot, session)
	expected := filepath.Join(townRoot, ".runtime", "nudge_poller", session+".pid")
	if pidFile != expected {
		t.Errorf("pollerPidFile() = %q, want %q", pidFile, expected)
	}
}

func TestPollerPidFile_SlashSanitized(t *testing.T) {
	townRoot := t.TempDir()
	session := "some/session"

	pidFile := pollerPidFile(townRoot, session)
	// Slashes should be replaced with underscores
	expected := filepath.Join(townRoot, ".runtime", "nudge_poller", "some_session.pid")
	if pidFile != expected {
		t.Errorf("pollerPidFile() = %q, want %q", pidFile, expected)
	}
}

func TestPollerAlive_NoPidFile(t *testing.T) {
	townRoot := t.TempDir()
	_, alive := pollerAlive(townRoot, "nonexistent-session")
	if alive {
		t.Error("pollerAlive() returned true for nonexistent PID file")
	}
}

func TestPollerAlive_StalePid(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	// Write a PID file with an invalid PID (process doesn't exist).
	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	// Use a very high PID that's almost certainly not running.
	if err := os.WriteFile(pidPath, []byte("999999999"), 0644); err != nil {
		t.Fatal(err)
	}

	_, alive := pollerAlive(townRoot, session)
	if alive {
		t.Error("pollerAlive() returned true for dead PID")
	}

	// Inspection does not mutate unverified ownership records.
}

func TestPollerAlive_CorruptPidFile(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	if err := os.WriteFile(pidPath, []byte("not-a-number"), 0644); err != nil {
		t.Fatal(err)
	}

	_, alive := pollerAlive(townRoot, session)
	if alive {
		t.Error("pollerAlive() returned true for corrupt PID file")
	}
}

func TestStopPoller_NoPidFile(t *testing.T) {
	townRoot := t.TempDir()
	// Should be a no-op, no error.
	if err := StopPoller(townRoot, "nonexistent"); err != nil {
		t.Errorf("StopPoller() unexpected error: %v", err)
	}
}

func TestStopPoller_StalePid(t *testing.T) {
	townRoot := t.TempDir()
	session := "gt-gastown-crew-test"

	// Write a stale PID file.
	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidPath := pollerPidFile(townRoot, session)
	if err := os.WriteFile(pidPath, []byte("999999999"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should succeed and clean up the stale PID file.
	if err := StopPoller(townRoot, session); err != nil {
		t.Errorf("StopPoller() unexpected error: %v", err)
	}

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("StopPoller did not clean up stale PID file")
	}
}

func TestPollerAliveRejectsUnrelatedLivePID(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(pollerPidDir(root), 0755); err != nil {
		t.Fatal(err)
	}
	path := pollerPidFile(root, "hq-mayor")
	// Legacy PID reuse must neither establish readiness nor authorize SIGTERM.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		t.Fatal(err)
	}
	if _, alive := pollerAlive(root, "hq-mayor"); alive {
		t.Fatal("unrelated live PID accepted")
	}
	if err := StopPoller(root, "hq-mayor"); err == nil {
		t.Fatal("unverified live PID stop accepted")
	}
	// Even forged matching PID/ready records cannot impersonate the process.
	record := pollerRecord{PID: os.Getpid(), Session: "hq-mayor", Token: "0123456789abcdef0123456789abcdef"}
	if err := writePollerRecord(path, record); err != nil {
		t.Fatal(err)
	}
	if err := writePollerRecord(path+".ready", record); err != nil {
		t.Fatal(err)
	}
	if _, alive := pollerAlive(root, "hq-mayor"); alive {
		t.Fatal("forged readiness accepted")
	}
	if err := StopPoller(root, "hq-mayor"); err == nil {
		t.Fatal("mismatched process stop accepted")
	}
	if !pollerProcessAlive(os.Getpid()) {
		t.Fatal("unrelated process was signaled")
	}
}

func TestPollerRealReadinessAndOwnedStop(t *testing.T) {
	binary := os.Getenv("GT_TEST_POLLER_BINARY")
	if binary == "" {
		t.Skip("run scripts/test-notification-delivery.py for real CLI readiness test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX terminal stub")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	original := pollerExecutable
	pollerExecutable = func() (string, error) { return binary, nil }
	t.Cleanup(func() { pollerExecutable = original })
	pid, err := StartPoller(root, "hq-mayor")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = StopPoller(root, "hq-mayor") })
	if got, alive := pollerAlive(root, "hq-mayor"); !alive || got != pid {
		t.Fatal("real child did not acknowledge readiness")
	}
	if got, err := StartPoller(root, "hq-mayor"); err != nil || got != pid {
		t.Fatalf("ready child not reused: %d %v", got, err)
	}
	path := pollerPidFile(root, "hq-mayor")
	record, err := readPollerRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	wrong := record
	wrong.Session = "other-session"
	if err := writePollerRecord(path, wrong); err != nil {
		t.Fatal(err)
	}
	if err := StopPoller(root, "hq-mayor"); err == nil {
		t.Fatal("session mismatch stop accepted")
	}
	if !pollerProcessMatches(pid, record.Session, record.Token) {
		t.Fatal("mismatched stop killed owned child")
	}
	if err := writePollerRecord(path, record); err != nil {
		t.Fatal(err)
	}
	if err := StopPoller(root, "hq-mayor"); err != nil {
		t.Fatal(err)
	}
	if _, alive := pollerAlive(root, "hq-mayor"); alive {
		t.Fatal("stopped child still ready")
	}
	// A launched child whose tmux target validation fails must not report success.
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := StartPoller(root, "missing-target"); err == nil {
		t.Fatal("failed child startup reported ready")
	}
}

func TestBuildPollerCommand_UsesDetachedProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group management is not supported on Windows")
	}
	townRoot := t.TempDir()
	cmd := buildPollerCommand("/tmp/fake-gt", townRoot, "gt-gastown-crew-bear")

	if got, want := cmd.Dir, townRoot; got != want {
		t.Fatalf("cmd.Dir = %q, want %q", got, want)
	}
	if got, want := cmd.Path, "/tmp/fake-gt"; got != want {
		t.Fatalf("cmd.Path = %q, want %q", got, want)
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "nudge-poller" || cmd.Args[2] != "gt-gastown-crew-bear" {
		t.Fatalf("cmd.Args = %#v, want poller invocation", cmd.Args)
	}
	if cmd.Cancel != nil {
		t.Fatal("buildPollerCommand() installed cmd.Cancel; detached pollers must leave it nil")
	}
	if cmd.Stdout != nil || cmd.Stderr != nil {
		t.Fatal("buildPollerCommand() should discard stdout/stderr")
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("buildPollerCommand() did not configure SysProcAttr")
	}
}

func TestSetProcessGroup_InstallsCancelHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SetProcessGroup is a no-op on Windows")
	}
	cmd := exec.Command("true")
	util.SetProcessGroup(cmd)

	if cmd.Cancel == nil {
		t.Fatal("SetProcessGroup() should install a cancel hook")
	}
}
