package convoy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/witness"
)

func setupRefusalCommands(t *testing.T, town, id string) (binary, logPath, state string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX stubs")
	}
	bin := t.TempDir()
	state = filepath.Join(bin, "state")
	if err := os.WriteFile(state, []byte("open"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"open", "blocked"} {
		data, _ := json.Marshal([]beads.Issue{{ID: id, Status: status, Description: `dispatch_constraints: {"agent":"codex","review_only":true,"no_merge":true,"hook_raw_bead":true}`}})
		if err := os.WriteFile(filepath.Join(bin, status+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
case "$1" in
 show) cat "$REFUSAL_BIN/$(cat "$REFUSAL_BIN/state").json" ;;
 update)
  test "$3" = "--status=blocked" || exit 9
  test "$#" = 3 || exit 9
  echo blocked > "$REFUSAL_BIN/state" ;;
 *) exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "bd"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REFUSAL_BIN", bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	binary, logPath = makeGTStub(t, 0)
	return
}

func TestTerminalRefusalPersistsAndAlertsOnceAcrossFeedScans(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "witness"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < config.DefaultWitnessMaxBeadRespawns; i++ {
		witness.RecordBeadRespawn(town, "test-latched")
	}
	binary, logPath, state := setupRefusalCommands(t, town, "test-latched")
	for i := 0; i < 3; i++ {
		if err := dispatchIssue(context.Background(), town, "test-latched", "rig", binary, ""); err == nil {
			t.Fatal("terminal dispatch accepted")
		}
	}
	status, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(status)) != "blocked" {
		t.Fatalf("refusal not durable: %q", status)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "escalate ") != 1 || strings.Contains(string(log), "sling ") {
		t.Fatalf("repeated alert or launch: %s", log)
	}
	if !strings.Contains(string(log), "--fingerprint convoy:respawn-limiter:test-latched") {
		t.Fatalf("missing durable alert identity: %s", log)
	}
	if !witness.ShouldBlockRespawn(town, "test-latched") {
		t.Fatal("intervention reset respawn counter")
	}
}

func TestTerminalRefusalDoesNotChangeNewAssignment(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "witness"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < config.DefaultWitnessMaxBeadRespawns; i++ {
		witness.RecordBeadRespawn(town, "test-active")
	}
	binary, logPath, state := setupRefusalCommands(t, town, "test-active")
	data, _ := json.Marshal([]beads.Issue{{ID: "test-active", Status: "open", Assignee: "rig/polecats/active"}})
	if err := os.WriteFile(filepath.Join(filepath.Dir(state), "open.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := HandleRespawnRefusal(context.Background(), town, "test-active", binary); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("new assignee received stale refusal alert: %v", err)
	}
	status, _ := os.ReadFile(state)
	if string(status) != "open" {
		t.Fatalf("new assignment changed: %s", status)
	}
}
