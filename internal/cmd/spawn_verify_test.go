package cmd

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/polecat"
)

// These tests exercise the FAILING path of dispatch verification (hq-a0f).
//
// The defect they guard is intermittent, so a green run here is NOT evidence
// that a live spawn works. What is covered, precisely:
//   - verifySessionLive's decision table, including the distinction between
//     "observed dead" and "could not observe" (fakes, no tmux);
//   - ensureBeadUnhooked's retry and its refusal to report success on an
//     unverified or still-hooked bead (fakes, no Dolt);
//   - the hooked-no-session classification in gt polecat list (pure function).
//
// NOT covered here, and deliberately: whether a real tmux spawn on a real box
// produces a live agent. That is the part that fails intermittently in
// production and no unit test can bound it.

// fakeLivenessProbe scripts a sequence of tmux answers.
type fakeLivenessProbe struct {
	hasSession    []bool
	hasSessionErr []error
	agentAlive    []bool
	agentAliveErr []error
	hasCalls      int
	aliveCalls    int
}

func pick[T any](vals []T, i int) (T, bool) {
	var zero T
	if len(vals) == 0 {
		return zero, false
	}
	if i >= len(vals) {
		return vals[len(vals)-1], true
	}
	return vals[i], true
}

func (f *fakeLivenessProbe) HasSession(string) (bool, error) {
	i := f.hasCalls
	f.hasCalls++
	if err, ok := pick(f.hasSessionErr, i); ok && err != nil {
		return false, err
	}
	v, ok := pick(f.hasSession, i)
	if !ok {
		return false, nil
	}
	return v, nil
}

func (f *fakeLivenessProbe) IsAgentAliveChecked(string) (bool, error) {
	i := f.aliveCalls
	f.aliveCalls++
	if err, ok := pick(f.agentAliveErr, i); ok && err != nil {
		return false, err
	}
	v, ok := pick(f.agentAlive, i)
	if !ok {
		return false, nil
	}
	return v, nil
}

func TestVerifySessionLive(t *testing.T) {
	tests := []struct {
		name    string
		probe   *fakeLivenessProbe
		wantErr error // nil, ErrSpawnNoSession, or errSessionUnverifiable
		wantMsg string
	}{
		{
			name:  "live-session-passes",
			probe: &fakeLivenessProbe{hasSession: []bool{true}, agentAlive: []bool{true}},
		},
		{
			name: "session-never-created-fails-loudly",
			// The reported shape: a spawn event is emitted, no session appears.
			probe:   &fakeLivenessProbe{hasSession: []bool{false}},
			wantErr: ErrSpawnNoSession,
			wantMsg: "does not exist",
		},
		{
			name: "session-exists-but-agent-dead-fails-loudly",
			// The silent shape: tmux session is up, the runtime inside it exited,
			// so getSessionPane succeeds and the old code reported success.
			probe:   &fakeLivenessProbe{hasSession: []bool{true}, agentAlive: []bool{false}},
			wantErr: ErrSpawnNoSession,
			wantMsg: "no agent process",
		},
		{
			name: "agent-comes-up-on-a-later-poll",
			probe: &fakeLivenessProbe{
				hasSession: []bool{true},
				agentAlive: []bool{false, false, true},
			},
		},
		{
			name: "session-appears-on-a-later-poll",
			probe: &fakeLivenessProbe{
				hasSession: []bool{false, false, true},
				agentAlive: []bool{true},
			},
		},
		{
			name: "tmux-unreachable-is-unverifiable-not-dead",
			// Must NOT be ErrSpawnNoSession: nothing was observed, so reporting
			// "the agent is dead" would be a negative with no bounds.
			probe:   &fakeLivenessProbe{hasSessionErr: []error{errors.New("tmux: no server running")}},
			wantErr: errSessionUnverifiable,
		},
		{
			name: "agent-probe-error-is-unverifiable-not-dead",
			probe: &fakeLivenessProbe{
				hasSession:    []bool{true},
				agentAliveErr: []error{errors.New("ps failed")},
			},
			wantErr: errSessionUnverifiable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifySessionLive(tt.probe, "gt-gastown-p-Toast", 60*time.Millisecond, 10*time.Millisecond)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("verifySessionLive() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("verifySessionLive() = %v, want error wrapping %v", err, tt.wantErr)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not explain the observation (want substring %q)", err, tt.wantMsg)
			}
		})
	}
}

func TestVerifySessionLiveRejectsNilProbeAsUnverifiable(t *testing.T) {
	err := verifySessionLive(nil, "gt-gastown-p-Toast", 10*time.Millisecond, time.Millisecond)
	if !errors.Is(err, errSessionUnverifiable) {
		t.Fatalf("nil probe = %v, want errSessionUnverifiable", err)
	}
}

func TestEnsureBeadUnhooked(t *testing.T) {
	tests := []struct {
		name         string
		unhookErrs   []error
		statuses     []string
		statusErrs   []error
		nilReader    bool
		wantErr      bool
		wantUnhooks  int
		wantErrMatch string
	}{
		{
			name:        "clean-unhook-verified-once",
			statuses:    []string{"open"},
			wantUnhooks: 1,
		},
		{
			name: "write-succeeds-but-bead-still-hooked-is-an-error",
			// The failure the old best-effort rollback swallowed: `bd update`
			// exits 0, the bead stays HOOKED, the polecat is removed anyway.
			statuses:     []string{"hooked", "hooked", "hooked"},
			wantErr:      true,
			wantUnhooks:  3,
			wantErrMatch: "still reads status=hooked",
		},
		{
			name:        "in-progress-also-counts-as-still-held",
			statuses:    []string{"in_progress", "in_progress", "in_progress"},
			wantErr:     true,
			wantUnhooks: 3,
		},
		{
			name:        "retries-until-the-unhook-lands",
			unhookErrs:  []error{errors.New("dolt: connection refused"), nil},
			statuses:    []string{"open"},
			wantUnhooks: 2,
		},
		{
			name:        "retries-until-the-status-read-succeeds",
			statusErrs:  []error{errors.New("dolt: timeout"), nil},
			statuses:    []string{"", "open"},
			wantUnhooks: 2,
		},
		{
			name:        "every-attempt-fails",
			unhookErrs:  []error{errors.New("boom")},
			wantErr:     true,
			wantUnhooks: 3,
		},
		{
			name:         "no-status-reader-reports-unverified-not-success",
			nilReader:    true,
			wantErr:      true,
			wantUnhooks:  1,
			wantErrMatch: "could not verify",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unhookCalls := 0
			unhook := func(string) error {
				i := unhookCalls
				unhookCalls++
				if err, ok := pick(tt.unhookErrs, i); ok {
					return err
				}
				return nil
			}
			readCalls := 0
			var read beadHookStateFn
			if !tt.nilReader {
				read = func(string) (string, error) {
					i := readCalls
					readCalls++
					if err, ok := pick(tt.statusErrs, i); ok && err != nil {
						return "", err
					}
					status, _ := pick(tt.statuses, i)
					return status, nil
				}
			}

			err := ensureBeadUnhooked("hq-a0f", unhook, read, unhookVerifyAttempts)
			if tt.wantErr && err == nil {
				t.Fatalf("ensureBeadUnhooked() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ensureBeadUnhooked() = %v, want nil", err)
			}
			if tt.wantErrMatch != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErrMatch)) {
				t.Errorf("error %v does not contain %q", err, tt.wantErrMatch)
			}
			if unhookCalls != tt.wantUnhooks {
				t.Errorf("unhook attempts = %d, want %d", unhookCalls, tt.wantUnhooks)
			}
		})
	}
}

func TestEnsureBeadUnhookedNoBeadIsANoop(t *testing.T) {
	called := false
	err := ensureBeadUnhooked("", func(string) error { called = true; return nil }, nil, 3)
	if err != nil {
		t.Fatalf("ensureBeadUnhooked(\"\") = %v, want nil", err)
	}
	if called {
		t.Error("ensureBeadUnhooked(\"\") attempted an unhook")
	}
}

func TestBeadStillHooked(t *testing.T) {
	held := []string{"hooked", "in_progress"}
	released := []string{"open", "closed", "blocked", "deferred", "pinned", ""}
	for _, status := range held {
		if !beadStillHooked(status) {
			t.Errorf("beadStillHooked(%q) = false, want true", status)
		}
	}
	for _, status := range released {
		if beadStillHooked(status) {
			t.Errorf("beadStillHooked(%q) = true, want false", status)
		}
	}
}

// TestDeadSessionWorkStateSeparatesNeverStartedFromDiedMidWork covers acceptance
// criterion 3: an operator must be able to tell "hooked but no session" from a
// session that died mid-work without reading tmux.
func TestDeadSessionWorkStateSeparatesNeverStartedFromDiedMidWork(t *testing.T) {
	if got := deadSessionWorkState("hooked"); got != polecat.StateHookedNoSession {
		t.Errorf("deadSessionWorkState(hooked) = %q, want %q", got, polecat.StateHookedNoSession)
	}
	for _, status := range []string{"in_progress", "open", "blocked", ""} {
		if got := deadSessionWorkState(status); got != polecat.StateStalled {
			t.Errorf("deadSessionWorkState(%q) = %q, want %q", status, got, polecat.StateStalled)
		}
	}
	// Boolean callers must keep working across the split.
	if !polecat.StateHookedNoSession.IsStalled() {
		t.Error("StateHookedNoSession.IsStalled() = false; existing stalled handling would skip it")
	}
	if polecat.StateHookedNoSession.IsWorking() || polecat.StateHookedNoSession.IsIdle() {
		t.Error("StateHookedNoSession must not read as working or idle")
	}
}

// TestHookedNoSessionIsNeverSafeToNuke pins the lifecycle policy: the new state
// must be treated exactly as conservatively as stalled by DecideWorkstate, which
// is what keeps patrol from destroying live work classified this way.
func TestHookedNoSessionIsNeverSafeToNuke(t *testing.T) {
	for _, state := range []polecat.State{polecat.StateStalled, polecat.StateHookedNoSession} {
		d := polecat.DecideWorkstate(polecat.WorkstateInput{
			State:             state,
			ActiveWorkBlocker: "assigned_work=hq-a0f status=hooked",
		})
		if d.SafeToNuke {
			t.Errorf("%s: SafeToNuke = true, want false", state)
		}
		if !d.NeedsRecovery {
			t.Errorf("%s: NeedsRecovery = false, want true", state)
		}
		if !d.CountsTowardCapacity {
			t.Errorf("%s: CountsTowardCapacity = false, want true", state)
		}
		if d.Verdict != polecat.WorkstateVerdictNeedsRecovery {
			t.Errorf("%s: Verdict = %q, want %q", state, d.Verdict, polecat.WorkstateVerdictNeedsRecovery)
		}
	}
}

// TestReportersNameTheBead keeps the loud failure messages actionable: an
// operator reading scrollback must get the bead id and a recovery command, not
// just "session failed".
func TestReportersNameTheBead(t *testing.T) {
	out := captureStdout(t, func() {
		reportDispatchFailure("gastown/polecats/Toast", "hq-a0f", fmt.Errorf("%w: gt-gastown-p-Toast", ErrSpawnNoSession))
	})
	for _, want := range []string{"DISPATCH FAILED", "gastown/polecats/Toast", "hq-a0f"} {
		if !strings.Contains(out, want) {
			t.Errorf("reportDispatchFailure output missing %q:\n%s", want, out)
		}
	}

	out = captureStdout(t, func() {
		reportStrandedBead("hq-a0f", errors.New("bead hq-a0f still reads status=hooked"))
	})
	for _, want := range []string{"STRANDED WORK", "hq-a0f", "bd update hq-a0f --status=open"} {
		if !strings.Contains(out, want) {
			t.Errorf("reportStrandedBead output missing %q:\n%s", want, out)
		}
	}
}
