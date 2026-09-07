package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/tmux"
)

type recordingNudgeTarget struct {
	busy     bool
	fail     bool
	messages []string
	sessions []string
}

func (r *recordingNudgeTarget) WaitForIdle(string, time.Duration) error {
	if r.busy {
		return tmux.ErrIdleTimeout
	}
	return nil
}
func (r *recordingNudgeTarget) NudgeSessionWithOpts(session, message string, opts tmux.NudgeOpts) error {
	if r.fail {
		return errors.New("terminal write failed")
	}
	r.messages = append(r.messages, message)
	r.sessions = append(r.sessions, session)
	return nil
}

func TestNotificationPollerDelivery(t *testing.T) {
	for _, agent := range []string{"codex", "claude", "gemini"} {
		t.Run(agent, func(t *testing.T) {
			root := t.TempDir()
			const target = "hq-mayor"
			if err := nudge.Enqueue(root, target, nudge.QueuedNudge{Sender: "test", Message: "delivery-marker"}); err != nil {
				t.Fatal(err)
			}
			if err := nudge.Enqueue(root, "other-session", nudge.QueuedNudge{Message: "untouched"}); err != nil {
				t.Fatal(err)
			}
			terminal := &recordingNudgeTarget{busy: true}
			detected := config.GetAgentPresetByName(agent).ReadyPromptPrefix != ""
			opts := tmux.NudgeOpts{TownRoot: root}
			if detected {
				if err := pollNudgeQueue(terminal, root, target, time.Millisecond, detected, opts); err != nil {
					t.Fatal(err)
				}
				if n, _ := nudge.Pending(root, target); n != 1 || len(terminal.messages) != 0 {
					t.Fatal("busy agent lost or received queued message")
				}
			}
			terminal.busy = false
			terminal.fail = true
			if err := pollNudgeQueue(terminal, root, target, time.Millisecond, detected, opts); err == nil {
				t.Fatal("expected injection failure")
			}
			if n, _ := nudge.Pending(root, target); n != 1 {
				t.Fatal("failed injection did not requeue")
			}
			terminal.fail = false
			if err := pollNudgeQueue(terminal, root, target, time.Millisecond, detected, opts); err != nil {
				t.Fatal(err)
			}
			if len(terminal.messages) != 1 || !strings.Contains(terminal.messages[0], "delivery-marker") || terminal.sessions[0] != target {
				t.Fatalf("delivery = %#v", terminal)
			}
			if n, _ := nudge.Pending(root, target); n != 0 {
				t.Fatal("successful injection left pending message")
			}
			if n, _ := nudge.Pending(root, "other-session"); n != 1 {
				t.Fatal("drained another session")
			}
			if err := pollNudgeQueue(terminal, root, target, time.Millisecond, detected, opts); err != nil {
				t.Fatal(err)
			}
			if len(terminal.messages) != 1 {
				t.Fatal("duplicate delivery")
			}
		})
	}
}

// Exercise the real sling/done producers and real CLI await-event cleanup,
// replacing only tmux with a nonexistent isolated socket. No bd calls occur.
func TestNotificationEventsDeliveryAndCleanup(t *testing.T) {
	setupNudgeTestRegistry(t)
	root := t.TempDir()
	for _, dir := range []string{"mayor", "alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, rig := range []string{"alpha", "beta"} {
		if err := os.WriteFile(filepath.Join(root, rig, "config.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	t.Setenv("GT_RIG", "")
	t.Setenv("GT_TEST_NUDGE_LOG", "")
	oldSocket := tmux.GetDefaultSocket()
	tmux.SetDefaultSocket("gt-notification-test-" + filepath.Base(root))
	t.Cleanup(func() { tmux.SetDefaultSocket(oldSocket) })
	oldChannel, oldRig, oldTimeout := awaitEventChannel, awaitEventRig, awaitEventTimeout
	oldQuiet, oldCleanup, oldJSON := awaitEventQuiet, awaitEventCleanup, moleculeJSON
	oldBead, oldBackoff := awaitEventAgentBead, awaitEventBackoffBase
	t.Cleanup(func() {
		awaitEventChannel, awaitEventRig, awaitEventTimeout = oldChannel, oldRig, oldTimeout
		awaitEventQuiet, awaitEventCleanup, moleculeJSON = oldQuiet, oldCleanup, oldJSON
		awaitEventAgentBead, awaitEventBackoffBase = oldBead, oldBackoff
	})
	awaitEventTimeout = "1ms"
	awaitEventQuiet, awaitEventCleanup, moleculeJSON = true, true, false
	awaitEventAgentBead, awaitEventBackoffBase = "", ""
	for _, tc := range []struct {
		channel, eventType string
		emit               func(string, string)
	}{
		{"refinery", "MQ_SUBMIT", nudgeRefinery}, {"witness", "POLECAT_DONE", nudgeWitness},
	} {
		t.Run(tc.channel, func(t *testing.T) {
			tc.emit("alpha", "alpha-marker")
			tc.emit("beta", "beta-marker")
			base := filepath.Join(root, "events", tc.channel)
			legacy := filepath.Join(base, "legacy.event")
			if err := os.WriteFile(legacy, []byte(`{"type":"LEGACY"}`), 0644); err != nil {
				t.Fatal(err)
			}
			alpha, err := readPendingEvents(filepath.Join(base, "alpha"))
			if err != nil {
				t.Fatal(err)
			}
			beta, err := readPendingEvents(filepath.Join(base, "beta"))
			if err != nil {
				t.Fatal(err)
			}
			if len(alpha) != 1 || len(beta) != 1 {
				t.Fatalf("producer counts: alpha=%d beta=%d", len(alpha), len(beta))
			}
			var event struct {
				Type    string
				Payload map[string]string
			}
			if err := json.Unmarshal(alpha[0].Content, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type != tc.eventType || event.Payload["rig"] != "alpha" || event.Payload["message"] != "alpha-marker" {
				t.Fatalf("wrong event: %+v", event)
			}
			legacyAlpha := filepath.Join(base, "attributed-alpha.event")
			legacyBeta := filepath.Join(base, "attributed-beta.event")
			for path, rig := range map[string]string{legacyAlpha: "alpha", legacyBeta: "beta"} {
				data := fmt.Sprintf(`{"channel":%q,"type":"LEGACY_WAKE","payload":{"rig":%q}}`, tc.channel, rig)
				if err := os.WriteFile(path, []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
			}
			awaitEventChannel, awaitEventRig = tc.channel, ""
			if err := runMoleculeAwaitEvent(moleculeAwaitEventCmd, nil); err == nil {
				t.Fatal("town-wide consumer accepted missing rig")
			}
			awaitEventRig = "alpha"
			awaitEventCleanup = false
			if err := runMoleculeAwaitEvent(moleculeAwaitEventCmd, nil); err != nil {
				t.Fatal(err)
			}
			migrated, err := readPendingEvents(filepath.Join(base, "alpha"))
			if err != nil || len(migrated) != 2 {
				t.Fatalf("legacy event not delivered with own scoped event: %v %v", migrated, err)
			}
			if _, err := os.Stat(legacyBeta); err != nil {
				t.Fatal("other rig legacy event stolen", err)
			}
			awaitEventCleanup = true
			if err := runMoleculeAwaitEvent(moleculeAwaitEventCmd, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(alpha[0].Path); !os.IsNotExist(err) {
				t.Fatal("own event not cleaned")
			}
			if _, err := os.Stat(beta[0].Path); err != nil {
				t.Fatal("other rig event deleted", err)
			}
			if _, err := os.Stat(legacy); err != nil {
				t.Fatal("legacy event deleted", err)
			}
			if _, err := os.Stat(legacyAlpha); !os.IsNotExist(err) {
				t.Fatal("legacy source not transferred")
			}
			if err := runMoleculeAwaitEvent(moleculeAwaitEventCmd, nil); err != nil {
				t.Fatal(err)
			}
			if again, err := readPendingEvents(filepath.Join(base, "alpha")); err != nil || len(again) != 0 {
				t.Fatal("legacy event delivered twice")
			}
			awaitEventRig = "beta"
			if err := runMoleculeAwaitEvent(moleculeAwaitEventCmd, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(legacyBeta); !os.IsNotExist(err) {
				t.Fatal("second rig legacy event not consumed")
			}
			if _, err := os.Stat(beta[0].Path); !os.IsNotExist(err) {
				t.Fatal("second rig could not consume its event")
			}
		})
	}
}

// Verify the queue reaches a real terminal reader over an isolated tmux socket.
// This is a synthetic Codex-style prompt, not a live model or production session.
func TestNotificationPollerTmuxDelivery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX terminal fixture")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	root := t.TempDir()
	script := filepath.Join(root, "reader.sh")
	content := "#!/bin/sh\nstty -echo\nprintf '› '\nwhile IFS= read -r line; do\n printf '%s\\n' \"$line\" >> received\n printf '\\033[2J\\033[H› '\ndone\n"
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("gt-notification-delivery-%d", os.Getpid())
	terminal := tmux.NewTmuxWithSocket(socket)
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	const target = "hq-mayor"
	if err := terminal.NewSessionWithCommandAndEnv(target, root, script, map[string]string{"GT_AGENT": "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := terminal.WaitForIdle(target, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := nudge.Enqueue(root, target, nudge.QueuedNudge{Sender: "test", Message: "terminal-delivery-marker"}); err != nil {
		t.Fatal(err)
	}
	if err := pollNudgeQueue(terminal, root, target, 3*time.Second, true, tmux.NudgeOpts{TownRoot: root, SkipEscape: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "received"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "terminal-delivery-marker") {
		t.Fatalf("terminal received %q", data)
	}
	if n, _ := nudge.Pending(root, target); n != 0 {
		t.Fatal("queue did not drain after real delivery")
	}
}

func TestNotificationEventCLIRecipient(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"mayor", "alpha", "beta"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "alpha", "config.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(root, "alpha"))
	t.Setenv("GT_RIG", "alpha")
	oldChannel, oldRig, oldType, oldPayload, oldJSON := emitEventChannel, emitEventRig, emitEventType, emitEventPayload, moleculeJSON
	t.Cleanup(func() {
		emitEventChannel, emitEventRig, emitEventType, emitEventPayload, moleculeJSON = oldChannel, oldRig, oldType, oldPayload, oldJSON
	})
	emitEventChannel, emitEventRig, emitEventType, emitEventPayload, moleculeJSON = "refinery", "beta", "PATROL_WAKE", []string{"source=witness"}, false
	if err := runMoleculeEmitEvent(moleculeEmitEventCmd, nil); err != nil {
		t.Fatal(err)
	}
	events, err := readPendingEvents(filepath.Join(root, "events", "refinery", "beta"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatal("explicit recipient did not override caller rig")
	}
	_, rig, err := resolveEventScope("refinery", "")
	if err != nil || rig != "alpha" {
		t.Fatalf("inferred scope %q: %v", rig, err)
	}
	if _, _, err := resolveEventScope("refinery", "../escape"); err == nil {
		t.Fatal("accepted rig traversal")
	}
}

// Exercise durable enqueue and actual StartPoller failure/idempotence without
// launching a detached process: an invalid PID directory fails; an existing
// live PID is reused. The test never signals that PID or touches a live town.
func TestNotificationQueueReportsPollerFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("GT_TEST_NUDGE_LOG", "")
	oldMode, oldPriority := nudgeModeFlag, nudgePriorityFlag
	t.Cleanup(func() { nudgeModeFlag, nudgePriorityFlag = oldMode, oldPriority })
	nudgeModeFlag, nudgePriorityFlag = NudgeModeQueue, nudge.PriorityNormal
	runtimeDir := filepath.Join(root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	pidDir := filepath.Join(runtimeDir, "nudge_poller")
	if err := os.WriteFile(pidDir, []byte("blocks mkdir"), 0644); err != nil {
		t.Fatal(err)
	}
	terminal := tmux.NewTmuxWithSocket("gt-notification-no-terminal")
	err := deliverNudge(terminal, "hq-mayor", "retained-on-failure", "test")
	if err == nil || !strings.Contains(err.Error(), "durably queued, but drain unavailable") {
		t.Fatalf("startup failure falsely succeeded: %v", err)
	}
	if n, err := nudge.Pending(root, "hq-mayor"); err != nil || n != 1 {
		t.Fatalf("queued item lost: count=%d err=%v", n, err)
	}
	if err := os.Remove(pidDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pidDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Positive coverage invokes the real CLI, so StartPoller's os.Executable
	// resolves to gt (never recursively to the go test binary).
	binary := os.Getenv("GT_TEST_POLLER_BINARY")
	if binary == "" {
		t.Skip("real CLI readiness requires notification test runner")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX terminal stub")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { _ = nudge.StopPoller(root, "hq-mayor") })
	var previousPID int
	for i := 0; i < 2; i++ {
		command := exec.Command(binary, "nudge", "mayor", "ready-poller-marker", "--mode=queue")
		command.Dir = root
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("queue CLI: %v: %s", err, out)
		}
		data, err := os.ReadFile(filepath.Join(pidDir, "hq-mayor.pid"))
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			PID   int
			Token string
		}
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.PID <= 0 || record.Token == "" {
			t.Fatal("missing owned poller identity")
		}
		if i > 0 && record.PID != previousPID {
			t.Fatal("already-ready poller was duplicated")
		}
		previousPID = record.PID
	}
	if n, err := nudge.Pending(root, "hq-mayor"); err != nil || n != 3 {
		t.Fatalf("durable queue count=%d err=%v", n, err)
	}
}
