// poller.go provides a background nudge-queue poller for agents that lack
// turn-boundary drain hooks (e.g., Gemini, Codex). Claude Code drains its
// queue via the UserPromptSubmit hook on every turn. Other runtimes have no
// equivalent hook, so queued nudges would sit undelivered forever.
//
// The poller runs as a background goroutine launched by crew/manager.Start().
// It polls the queue every PollInterval, waits for the agent to be idle, then
// drains and injects the formatted nudges via tmux NudgeSession.
//
// Lifecycle: StartPoller() → background loop → StopPoller() (or session death).
// A PID file at <townRoot>/.runtime/nudge_poller/<session>.pid allows Stop()
// to clean up even if the original manager has been replaced.
package nudge

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/util"
)

// Poller tuning defaults (overridable via flags or tests).
var (
	// DefaultPollInterval is how often the poller checks the queue.
	DefaultPollInterval = "10s"
	// DefaultIdleTimeout is how long to wait for the agent to become idle
	// before skipping this poll cycle and trying again next interval.
	DefaultIdleTimeout = "3s"
)

// pollerPidDir returns the directory for poller PID files.
func pollerPidDir(townRoot string) string {
	return filepath.Join(townRoot, constants.DirRuntime, "nudge_poller")
}

// pollerPidFile returns the PID file path for a session's poller.
func pollerPidFile(townRoot, session string) string {
	safe := strings.ReplaceAll(session, "/", "_")
	return filepath.Join(pollerPidDir(townRoot), safe+".pid")
}

// pollerRecord binds a process to one launch, session and readiness handshake.
// A bare PID is insufficient: the OS may reuse it for an unrelated process.
type pollerRecord struct {
	PID     int    `json:"pid"`
	Session string `json:"session"`
	Token   string `json:"token"`
}

var pollerExecutable = os.Executable

func readPollerRecord(path string) (pollerRecord, error) {
	var r pollerRecord
	data, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	token, err := hex.DecodeString(r.Token)
	if err != nil || len(token) != 16 || r.PID <= 0 || r.Session == "" {
		return r, fmt.Errorf("invalid poller ownership record")
	}
	return r, nil
}

func writePollerRecord(path string, r pollerRecord) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".poller-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// StartPoller starts (or reuses) an owned, ready nudge-poller. A successful
// exec alone is not readiness: the child must verify its target and acknowledge
// this launch token. Concurrent starters serialize before inspecting ownership.
func StartPoller(townRoot, session string) (int, error) {
	pidDir := pollerPidDir(townRoot)
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		return 0, fmt.Errorf("creating poller pid dir: %w", err)
	}
	pidPath := pollerPidFile(townRoot, session)
	unlock, err := lock.FlockAcquire(pidPath + ".lock")
	if err != nil {
		return 0, err
	}
	defer unlock()
	if pid, alive := pollerAlive(townRoot, session); alive {
		return pid, nil
	}
	// Never kill a process named only by stale/unverified state. A fresh launch
	// replaces that state only after obtaining a new unpredictable token.
	gtBin, err := pollerExecutable()
	if err != nil {
		return 0, fmt.Errorf("finding gt binary: %w", err)
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return 0, err
	}
	token := hex.EncodeToString(tokenBytes)
	cmd := buildPollerCommand(gtBin, townRoot, session)
	cmd.Args = append(cmd.Args, "--owner-token", token)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting nudge-poller: %w", err)
	}
	r := pollerRecord{PID: cmd.Process.Pid, Session: session, Token: token}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	cleanupFailed := func() {
		// This handle belongs to the child we just spawned, never to a PID file.
		_ = cmd.Process.Kill()
		removePollerRecord(pidPath, r)
		removePollerRecord(pidPath+".ready", r)
	}
	if err := writePollerRecord(pidPath, r); err != nil {
		cleanupFailed()
		return 0, err
	}
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-exited:
			cleanupFailed()
			return 0, fmt.Errorf("nudge-poller exited before readiness: %v", err)
		case <-timeout.C:
			cleanupFailed()
			return 0, fmt.Errorf("nudge-poller readiness timed out for %s", session)
		case <-ticker.C:
			if pid, alive := pollerAlive(townRoot, session); alive && pid == r.PID {
				return pid, nil
			}
		}
	}
}

func buildPollerCommand(gtBin, townRoot, session string) *exec.Cmd {
	cmd := exec.Command(gtBin, "nudge-poller", session)
	cmd.Dir = townRoot
	cmd.Stdout = nil
	cmd.Stderr = nil
	util.SetDetachedProcessGroup(cmd)
	return cmd
}

// MarkPollerReady is called by the child only after target/session validation.
// Empty token supports manually invoked diagnostic pollers without claiming an
// automatically managed process. The returned cleanup removes only this token.
func MarkPollerReady(townRoot, session, token string) (func(), error) {
	if token == "" {
		return func() {}, nil
	}
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 16 {
		return nil, fmt.Errorf("invalid poller owner token")
	}
	r := pollerRecord{PID: os.Getpid(), Session: session, Token: token}
	path := pollerPidFile(townRoot, session)
	if err := writePollerRecord(path+".ready", r); err != nil {
		return nil, err
	}
	return func() { removePollerRecord(path+".ready", r); removePollerRecord(path, r) }, nil
}

func removePollerRecord(path string, r pollerRecord) {
	if current, err := readPollerRecord(path); err == nil && current == r {
		_ = os.Remove(path)
	}
}

// StopPoller refuses to signal unverified live PIDs, including legacy bare PID
// files. Identity is checked again immediately before signaling the owned child.
func StopPoller(townRoot, session string) error {
	path := pollerPidFile(townRoot, session)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	unlock, err := lock.FlockAcquire(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	r, err := readPollerRecord(path)
	if err != nil {
		// Legacy dead PID/corrupt files can be removed, but live bare PIDs cannot
		// establish identity and must never be sent a signal.
		if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pollerProcessAlive(pid) {
			return fmt.Errorf("refusing to signal unverified legacy poller PID %d", pid)
		}
		_ = os.Remove(path)
		return nil
	}
	if !pollerProcessAlive(r.PID) {
		removePollerRecord(path, r)
		removePollerRecord(path+".ready", r)
		return nil
	}
	// Obtain the process handle before checking identity. On Linux Go uses a
	// pidfd, so a later PID reuse cannot redirect the signal to a new process.
	proc, err := os.FindProcess(r.PID)
	if err != nil {
		return err
	}
	defer proc.Release()
	if r.Session != session || !pollerProcessMatches(r.PID, session, r.Token) {
		return fmt.Errorf("refusing to signal PID %d: nudge-poller identity mismatch", r.PID)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	removePollerRecord(path, r)
	removePollerRecord(path+".ready", r)
	return nil
}

func pollerAlive(townRoot, session string) (int, bool) {
	path := pollerPidFile(townRoot, session)
	r, err := readPollerRecord(path)
	if err != nil || r.Session != session {
		return 0, false
	}
	ready, err := readPollerRecord(path + ".ready")
	if err != nil || ready != r || !pollerProcessMatches(r.PID, session, r.Token) {
		return 0, false
	}
	return r.PID, true
}

// Watcher provides a filesystem-event-driven interface to the nudge queue.
// This is an ACP-safe alternative to polling and is preferred for long-running
// watchers like ACP Propeller.
type Watcher struct {
	townRoot string
	session  string
	dir      string
	closed   chan struct{}
	wg       sync.WaitGroup
	events   chan struct{}
}

// NewWatcher creates a new watcher for the given town root and session.
// The watcher observes nudge queue writes and signals via the Events() channel.
func NewWatcher(townRoot, session string) (*Watcher, error) {
	dir := queueDir(townRoot, session)
	// Ensure the directory exists so watch can start immediately.
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating nudge queue dir: %w", err)
	}

	w := &Watcher{
		townRoot: townRoot,
		session:  session,
		dir:      dir,
		closed:   make(chan struct{}),
		events:   make(chan struct{}, 1), // buffer one signal for coalescing
	}

	w.wg.Add(1)
	go w.watch()
	return w, nil
}

// Events returns a channel that receives a struct{} when the queue may have
// changed. Multiple changes within a short window are coalesced.
func (w *Watcher) Events() <-chan struct{} {
	return w.events
}

// Close stops the watcher and releases resources.
func (w *Watcher) Close() error {
	select {
	case <-w.closed:
		return fmt.Errorf("watcher already closed")
	default:
	}
	close(w.closed)
	w.wg.Wait()
	return nil
}

func (w *Watcher) watch() {
	defer w.wg.Done()

	// Use fsnotify directly.
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		// Log but don't block; fallback behavior is explicit in callers.
		fmt.Fprintf(os.Stderr, "nudge watcher init failed for %s: %v\n", w.dir, err)
		return
	}
	defer func() { _ = watcher.Close() }()

	// Watch the directory.
	if err := watcher.Add(w.dir); err != nil {
		fmt.Fprintf(os.Stderr, "nudge watcher failed to add dir %s: %v\n", w.dir, err)
		return
	}

	// Coalescing window.
	coalesceTimer := time.NewTicker(100 * time.Millisecond)
	defer coalesceTimer.Stop()

	pending := false
	for {
		select {
		case <-w.closed:
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Only care about file creation/modification in the queue dir
			if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
				// Filter: only .json files in the queue directory
				if strings.HasSuffix(event.Name, ".json") && filepath.Dir(event.Name) == w.dir {
					pending = true
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "nudge watcher error: %v\n", err)
		case <-coalesceTimer.C:
			if pending {
				pending = false
				select {
				case w.events <- struct{}{}:
				default:
				}
			}
		}
	}
}

// WatcherForSession returns a Watcher for a specific session or an error if
// creation fails (e.g., filesystem issues). Callers should handle cleanup.
func WatcherForSession(townRoot, session string) (*Watcher, error) {
	return NewWatcher(townRoot, session)
}
