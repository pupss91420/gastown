package convoy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/lock"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/witness"
)

// HandleRespawnRefusal makes a latched refusal visible and durable. Both feeder
// paths share this transaction. Fingerprinted escalation precedes the status
// write, so a crash cannot silently leave blocked work without an alert. A retry
// after a partial failure deduplicates the alert through the escalation system.
func HandleRespawnRefusal(ctx context.Context, townRoot, issueID, gtPath string, convoyIDs ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Share the CLI/queue per-bead lock so intervention cannot race a sling
	// that has already read the open state and is about to assign a worker.
	dir := filepath.Join(townRoot, ".runtime", "locks", "sling")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	key := strings.NewReplacer("/", "_", ":", "_").Replace(issueID) + ".flock"
	unlock, locked, err := lock.FlockTryAcquire(filepath.Join(dir, key))
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer unlock()
	if !witness.ShouldBlockRespawn(townRoot, issueID) {
		return nil
	}
	fallback := filepath.Join(townRoot, ".beads")
	read := func() (*beads.Issue, error) {
		data, err := beads.CommandContext(ctx, townRoot, fallback, beads.ReadOnlyRouting, "show", "--json", issueID).Output()
		if err != nil {
			return nil, fmt.Errorf("reading terminal refusal state: %w", err)
		}
		var items []beads.Issue
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, err
		}
		if len(items) != 1 || items[0].ID != issueID {
			return nil, fmt.Errorf("terminal refusal read returned wrong bead for %s", issueID)
		}
		return &items[0], nil
	}
	issue, err := read()
	if err != nil {
		return err
	}
	// Do not disturb active assignees, completed work or operator pauses.
	if issue.Status != "open" || issue.Assignee != "" {
		return nil
	}
	convoyID := "unknown"
	if len(convoyIDs) > 0 && convoyIDs[0] != "" {
		convoyID = convoyIDs[0]
	}
	reason := fmt.Sprintf("Convoy %s: automatic dispatch stopped for %s: respawn limit reached. Investigate startup failure before reopening and resetting the counter. Runtime and review constraints remain on the bead.", convoyID, issueID)
	alert := exec.CommandContext(ctx, gtPath, "escalate", "Dispatch requires intervention: "+issueID, "--severity", "high", "--source", "convoy:respawn-limiter", "--related", issueID, "--fingerprint", "convoy:respawn-limiter:"+issueID, "--reason", reason, "--json")
	alert.Dir = townRoot
	alert.Env = beads.BuildMutationRoutingBDEnv(os.Environ(), fallback)
	util.SetProcessGroup(alert)
	var stderr bytes.Buffer
	alert.Stderr = &stderr
	out, err := alert.Output()
	if err != nil {
		return fmt.Errorf("escalating terminal refusal: %w: %s", err, stderr.String())
	}
	var response struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &response); err != nil || response.ID == "" {
		return fmt.Errorf("unverified escalation result for %s: %s", issueID, out)
	}
	if response.Status != "ok" && response.Status != "partial_failure" && response.Status != "duplicate_suppressed" {
		return fmt.Errorf("unexpected escalation status %q", response.Status)
	}
	// Exit zero can mean partial_failure or duplicate_suppressed. Neither is
	// delivery evidence. Inspect durable mailbox receipts, including read mail.
	present, err := hasRefusalNotification(ctx, townRoot, response.ID)
	if err != nil {
		return err
	}
	if !present {
		if err := sendRefusalNotification(townRoot, response.ID, reason); err != nil {
			return fmt.Errorf("terminal notification pending: %w", err)
		}
		present, err = hasRefusalNotification(ctx, townRoot, response.ID)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("terminal notification receipt missing for %s", response.ID)
		}
	}
	// Recheck after escalation before changing status; another actor may have
	// paused or assigned the bead while notification was in flight.
	issue, err = read()
	if err != nil {
		return err
	}
	if issue.Status != "open" || issue.Assignee != "" {
		return nil
	}
	if out, err := beads.CommandContext(ctx, townRoot, fallback, beads.MutationRouting, "update", issueID, "--status=blocked", "--append-notes="+reason+" Escalation: "+response.ID+"; coordinator mailbox receipt verified.").CombinedOutput(); err != nil {
		return fmt.Errorf("persisting terminal refusal: %w: %s", err, out)
	}
	issue, err = read()
	if err != nil {
		return err
	}
	if issue.Status != "blocked" {
		return fmt.Errorf("terminal refusal did not persist for %s: status=%s", issueID, issue.Status)
	}
	return nil
}

func hasRefusalNotification(ctx context.Context, townRoot, escalationID string) (bool, error) {
	out, err := beads.CommandContext(ctx, townRoot, filepath.Join(townRoot, ".beads"), beads.ReadOnlyPinned, "message", "thread", escalationID, "--json").Output()
	if err != nil {
		return false, fmt.Errorf("checking terminal notification receipt: %w", err)
	}
	var receipts []mail.BeadsMessage
	if err := json.Unmarshal(out, &receipts); err != nil {
		return false, fmt.Errorf("invalid terminal notification receipt: %w", err)
	}
	for _, receipt := range receipts {
		message := receipt.ToMessage()
		if message.ID != "" && message.ThreadID == escalationID && mail.AddressToIdentity(message.To) == mail.AddressToIdentity("mayor/") {
			return true, nil
		}
	}
	return false, nil
}

var sendRefusalNotification = func(townRoot, escalationID, reason string) error {
	router := mail.NewRouter(townRoot)
	defer router.WaitPendingNotifications()
	return router.Send(&mail.Message{From: "convoy", To: "mayor/", Subject: "Dispatch requires intervention", Body: reason + "\nEscalation: " + escalationID, ThreadID: escalationID, Type: mail.TypeEscalation, Priority: mail.PriorityHigh})
}
