package cmd

import (
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/gastown/internal/style"
)

// Dispatch verification (hq-a0f).
//
// A polecat dispatch is two independent facts: the bead is HOOKED to an agent,
// and that agent has a live session. Until hq-a0f these were never checked
// together. `spawn` is logged at allocation time — before any tmux session
// exists (polecat_spawn.go) — and the only proof-of-life checks were one-shot:
// two `pane_dead` probes 250ms apart inside tmux session creation, one
// HasSession/CheckSessionHealth pair at the end of SessionManager.Start, and a
// getSessionPane call. A runtime that came up and exited a second later passed
// all of them, so sling reported success over an agent that does not exist and
// the work sat hooked to nobody.
//
// The helpers here close that gap: verifySessionLive proves the session is live
// after startup settles, and ensureBeadUnhooked proves the rollback actually
// released the bead instead of assuming a best-effort `bd update` landed.

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
// Callers must fail the dispatch on ErrSpawnNoSession and may warn-and-continue
// on errSessionUnverifiable, which is what keeps a tmux-less environment from
// reading as a fleet of dead agents.
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

// unhookBeadFn performs one unhook attempt against a bead.
type unhookBeadFn func(beadID string) error

// beadHookStateFn reports a bead's current status, so a rollback can confirm the
// unhook landed instead of trusting the write's exit code.
type beadHookStateFn func(beadID string) (status string, err error)

// beadStillHooked reports whether a status string still holds the bead against
// an agent. Both hooked and in_progress keep work assigned and out of `bd list
// --status open`, so neither counts as released.
func beadStillHooked(status string) bool {
	switch status {
	case "hooked", "in_progress":
		return true
	default:
		return false
	}
}

// ensureBeadUnhooked runs unhook and verifies the bead actually left hooked
// state, retrying up to attempts times.
//
// The verification is the point. A rollback that reports success on the exit
// code of a `bd update` leaves work hooked to a polecat that is about to be
// removed — the exact "hooked to an agent that does not exist" state hq-a0f is
// about. If the bead cannot be confirmed released, that is a hard error so the
// caller can say so loudly rather than exiting quietly.
func ensureBeadUnhooked(beadID string, unhook unhookBeadFn, readState beadHookStateFn, attempts int) error {
	if beadID == "" {
		return nil
	}
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := unhook(beadID); err != nil {
			lastErr = fmt.Errorf("unhook attempt %d: %w", attempt, err)
			continue
		}
		if readState == nil {
			// No way to verify. Report it as unverified rather than as success —
			// "not checked" and "checked and clean" must not read the same.
			return fmt.Errorf("unhooked %s but could not verify: no status reader", beadID)
		}
		status, err := readState(beadID)
		if err != nil {
			lastErr = fmt.Errorf("verify attempt %d: %w", attempt, err)
			continue
		}
		if !beadStillHooked(status) {
			return nil
		}
		lastErr = fmt.Errorf("verify attempt %d: bead %s still reads status=%s", attempt, beadID, status)
	}
	return lastErr
}

// reportStrandedBead prints the loud, unmissable failure for a bead that a
// rollback could not release. This is the one outcome an operator must never
// have to discover by reading tmux.
func reportStrandedBead(beadID string, err error) {
	fmt.Printf("\n%s %s\n", style.Error.Render("✗ STRANDED WORK:"),
		fmt.Sprintf("bead %s could not be unhooked during rollback: %v", beadID, err))
	fmt.Printf("  It is still assigned to an agent that is being torn down and no session will run it.\n")
	fmt.Printf("  Recover with: bd update %s --status=open --assignee=\n\n", beadID)
}

// reportDispatchFailure prints the loud, unmissable failure for a dispatch whose
// session never came up. Acceptance criterion 1 of hq-a0f: a spawn that does not
// produce a live session must fail at the call site, in terms an operator cannot
// mistake for the "✓ Work attached" line printed moments earlier.
func reportDispatchFailure(agentID, beadID string, err error) {
	fmt.Printf("\n%s %s\n", style.Error.Render("✗ DISPATCH FAILED:"),
		fmt.Sprintf("%s has no live session — %v", agentID, err))
	if beadID != "" {
		fmt.Printf("  Bead %s was NOT dispatched. Rolling back so it does not sit hooked to a dead agent.\n", beadID)
	}
}
