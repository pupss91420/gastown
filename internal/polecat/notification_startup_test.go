package polecat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestNotificationPolecatPollerFailureDoesNotFailSpawn(t *testing.T) {
	setupTestRegistryForSession(t)
	root, assertOneLive := testutil.NotificationStartupFixture(t, "gt-alpha")
	rigPath := filepath.Join(root, "gastown")
	work := filepath.Join(rigPath, "polecats", "alpha", "gastown")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	manager := NewSessionManager(tmux.NewTmux(), &rig.Rig{Name: "gastown", Path: rigPath})
	opts := SessionStartOptions{Agent: "codex", WorkDir: work, Command: "sleep 30"}
	if err := manager.Start("alpha", opts); err != nil {
		t.Fatalf("live polecat reported spawn failure: %v", err)
	}
	assertOneLive()
	if err := manager.Start("alpha", opts); !errors.Is(err, ErrSessionRunning) {
		t.Fatalf("retry lost already-running classification: %v", err)
	}
	assertOneLive()
}
