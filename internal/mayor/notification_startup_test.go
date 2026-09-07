package mayor

import (
	"errors"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
)

func TestNotificationMayorPollerFailureDoesNotFailSpawn(t *testing.T) {
	root, assertOneLive := testutil.NotificationStartupFixture(t, SessionName())
	manager := NewManager(root)
	if err := manager.Start("codex"); err != nil {
		t.Fatalf("live mayor reported spawn failure: %v", err)
	}
	assertOneLive()
	if err := manager.Start("codex"); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("retry lost already-running classification: %v", err)
	}
	assertOneLive()
}
