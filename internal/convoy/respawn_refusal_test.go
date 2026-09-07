package convoy

import (
	"context"
	"encoding/json"
	"fmt"
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
 query) cat "$REFUSAL_BIN/mail.json" ;;
 update)
  test "$3" = "--status=blocked" || exit 9
  test "$#" = 4 || exit 9
  case "$4" in --append-notes=*) ;; *) exit 9 ;; esac
  echo blocked > "$REFUSAL_BIN/state" ;;
 *) exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "bd"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REFUSAL_BIN", bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	receipt := `[{"id":"hq-message","assignee":"mayor","labels":["thread:hq-refusal"]}]`
	if err := os.WriteFile(filepath.Join(bin, "mail.json"), []byte(receipt), 0600); err != nil {
		t.Fatal(err)
	}
	logPath = filepath.Join(bin, "alerts")
	binary = filepath.Join(bin, "gt")
	gtScript := `#!/bin/sh
printf '%s\n' "$*" >> "$REFUSAL_BIN/alerts"
echo '{"id":"hq-refusal","status":"ok"}'
`
	if err := os.WriteFile(binary, []byte(gtScript), 0755); err != nil {
		t.Fatal(err)
	}
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

func TestTerminalRefusalRetriesUndeliveredDuplicate(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "witness"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < config.DefaultWitnessMaxBeadRespawns; i++ {
		witness.RecordBeadRespawn(town, "test-retry")
	}
	binary, _, state := setupRefusalCommands(t, town, "test-retry")
	mailPath := filepath.Join(filepath.Dir(state), "mail.json")
	receipt, err := os.ReadFile(mailPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mailPath, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	// gt escalate's first call creates a bead but exits zero after mail failure;
	// subsequent calls suppress the same escalation without retrying mail.
	response := filepath.Join(filepath.Dir(state), "response.json")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncat \"$REFUSAL_BIN/response.json\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(response, []byte(`{"id":"hq-refusal","status":"partial_failure"}`), 0600); err != nil {
		t.Fatal(err)
	}
	previousSend := sendRefusalNotification
	t.Cleanup(func() { sendRefusalNotification = previousSend })
	attempts, persisted := 0, 0
	sendRefusalNotification = func(root, id, reason string) error {
		attempts++
		if attempts == 1 {
			return fmt.Errorf("mail transport unavailable")
		}
		if id != "hq-refusal" || !strings.Contains(reason, "hq-convoy") {
			t.Fatalf("notification lost identity/context: %s %s", id, reason)
		}
		persisted++
		return os.WriteFile(mailPath, receipt, 0600)
	}
	if err := HandleRespawnRefusal(context.Background(), town, "test-retry", binary, "hq-convoy"); err == nil {
		t.Fatal("zero exit falsely treated as delivered")
	}
	status, _ := os.ReadFile(state)
	if string(status) != "open" {
		t.Fatalf("failed notification made retry unreachable: %s", status)
	}
	if err := os.WriteFile(response, []byte(`{"id":"hq-refusal","status":"duplicate_suppressed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := HandleRespawnRefusal(context.Background(), town, "test-retry", binary, "hq-convoy"); err != nil {
			t.Fatal(err)
		}
	}
	status, _ = os.ReadFile(state)
	if strings.TrimSpace(string(status)) != "blocked" || attempts != 2 || persisted != 1 {
		t.Fatalf("retry/dedup failed: status=%s attempts=%d persisted=%d", status, attempts, persisted)
	}
}

func TestTerminalRefusalRequiresReceiptAfterSuccessfulSend(t *testing.T) {
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "witness"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < config.DefaultWitnessMaxBeadRespawns; i++ {
		witness.RecordBeadRespawn(town, "test-missing")
	}
	binary, _, state := setupRefusalCommands(t, town, "test-missing")
	if err := os.WriteFile(filepath.Join(filepath.Dir(state), "mail.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	previousSend := sendRefusalNotification
	t.Cleanup(func() { sendRefusalNotification = previousSend })
	sendRefusalNotification = func(string, string, string) error { return nil }
	err := HandleRespawnRefusal(context.Background(), town, "test-missing", binary)
	if err == nil || !strings.Contains(err.Error(), "receipt missing") {
		t.Fatalf("unverified send accepted: %v", err)
	}
	status, _ := os.ReadFile(state)
	if string(status) != "open" {
		t.Fatalf("missing receipt suppressed retry: %s", status)
	}
}
