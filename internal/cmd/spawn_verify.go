package cmd

import (
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// ErrSpawnNoSession reports that a spawn completed without producing a live
// agent session. Callers must treat this as fatal to the dispatch: the hook
// must be rolled back and the failure surfaced, never logged as a warning.
var ErrSpawnNoSession = errors.New("spawn produced no live agent session")

// errSessionUnverifiable reports that liveness could not be determined at all —
// tmux itself was unreachable, so we observed nothing rather than observing an
// absence. This is deliberately NOT ErrSpawnNoSession: a tmux outage must not be
// reported as "the agent is dead", and a negative whose bounds are unknown must
// not be treated as evidence.
var errSessionUnverifiable = errors.New("session liveness could not be verified")

const (
	// spawnLivenessTimeout bounds how long verifySessionLive waits for a freshly
	// started session to prove it is live. It runs after SessionManager.Start has
	// already waited for the runtime prompt, so this covers the settle window
	// where a runtime that started can still exit.
	spawnLivenessTimeout = 15 * time.Second

	// spawnLivenessInterval is the poll interval within spawnLivenessTimeout.
	spawnLivenessInterval = 500 * time.Millisecond

	// unhookVerifyAttempts bounds how many times a rollback re-tries the unhook
	// before declaring the bead stranded. Dolt writes on this box fail
	// intermittently, and a rollback that silently loses is the whole defect.
	unhookVerifyAttempts = 3
)

// sessionLivenessProbe is the tmux surface needed to prove an agent session is
// live. *tmux.Tmux satisfies it; tests supply a fake.
type sessionLivenessProbe interface {
	HasSession(session string) (bool, error)
	IsAgentAliveChecked(session string) (bool, error)
}

// verifySessionLive polls until the tmux session exists AND the agent process
// inside it is alive at the same observation, or until timeout.
//
// Return values are three-way on purpose:
//   - nil: session exists and the agent is running.
//   - ErrSpawnNoSession: tmux answered, and the answer was "no live agent".
//   - errSessionUnverifiable: tmux never answered, so nothing was observed.
//
// Both errors fail dispatch; unknown liveness must never be reported as death.
func verifySessionLive(probe sessionLivenessProbe, sessionName string, timeout, interval time.Duration) error {
	if probe == nil || sessionName == "" {
		return fmt.Errorf("%w: no probe or session name", errSessionUnverifiable)
	}
	if interval <= 0 {
		interval = spawnLivenessInterval
	}

	deadline := time.Now().Add(timeout)
	// lastReason describes the most recent *observation*; probeErr records that we
	// could not observe at all. Only one of the two decides the outcome.
	lastReason := ""
	var probeErr error

	for {
		exists, err := probe.HasSession(sessionName)
		switch {
		case err != nil:
			probeErr = err
			lastReason = ""
		case !exists:
			probeErr = nil
			lastReason = "tmux session does not exist"
		default:
			alive, aliveErr := probe.IsAgentAliveChecked(sessionName)
			if aliveErr != nil {
				probeErr = aliveErr
				lastReason = ""
			} else if alive {
				return nil
			} else {
				probeErr = nil
				lastReason = "tmux session exists but no agent process is running in it"
			}
		}

		if !time.Now().Add(interval).Before(deadline) {
			break
		}
		time.Sleep(interval)
	}

	if lastReason == "" && probeErr != nil {
		return fmt.Errorf("%w: %s: %v", errSessionUnverifiable, sessionName, probeErr)
	}
	if lastReason == "" {
		lastReason = "no observation recorded"
	}
	return fmt.Errorf("%w: %s: %s (waited %s)", ErrSpawnNoSession, sessionName, lastReason, timeout)
}

// verifyRollbackUnhook retries a lost write while re-reading owner intent before
// every attempt. Pauses remain paused; completed or reassigned work is untouched.
func verifyRollbackUnhook(beadID, agentID string, read func(string) (*beadInfo, error), unhook func(string, string) error) error {
	var lastErr error
	for attempt := 0; attempt < unhookVerifyAttempts; attempt++ {
		current, err := read(beadID)
		if err != nil || current == nil {
			return fmt.Errorf("cannot read current hook: %v", err)
		}
		status, allowed := rollbackUnhookStatus(current, agentID)
		if !allowed {
			// Only a known terminal state or another owner supersedes this
			// dispatch. An unknown state still assigned here is not release.
			if (current.Assignee != "" && current.Assignee != agentID) || beads.IssueStatus(current.Status).IsTerminal() {
				return nil
			}
			return fmt.Errorf("cannot release status=%s assignee=%s", current.Status, current.Assignee)
		}
		if current.Assignee == "" && current.Status == status {
			return nil
		}
		if err := unhook(beadID, status); err != nil {
			lastErr = err
			continue
		}
		observed, err := read(beadID)
		if err == nil && observed != nil && observed.Status == status && observed.Assignee == "" {
			return nil
		}
		lastErr = fmt.Errorf("unhook not confirmed: %v", err)
	}
	return lastErr
}

func reportDispatchFailure(agentID, beadID string, err error) {
	fmt.Printf("\nDISPATCH FAILED: %s for bead %s: %v; rolling back partial dispatch.\n", agentID, beadID, err)
}
