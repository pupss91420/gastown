package cmd

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Liveness observations are scripted; these tests do not launch real agents.

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

func TestVerifyRollbackUnhook(t *testing.T) {
	const agent = "gastown/polecats/probe"
	for _, scenario := range []string{"success", "lost write", "write error", "read error", "still hooked", "pause", "reassigned", "closed", "unknown status", "nil read", "pause during retry"} {
		t.Run(scenario, func(t *testing.T) {
			state := &beadInfo{Status: "hooked", Assignee: agent}
			writes := 0
			if scenario == "pause" {
				state.Status = "blocked"
			}
			if scenario == "reassigned" {
				state.Assignee = "other"
			}
			if scenario == "closed" {
				state.Status = "closed"
			}
			if scenario == "unknown status" {
				state.Status = "mystery"
			}
			read := func(string) (*beadInfo, error) {
				if scenario == "read error" {
					return nil, errors.New("unavailable")
				}
				if scenario == "nil read" {
					return nil, nil
				}
				copy := *state
				return &copy, nil
			}
			write := func(_ string, status string) error {
				writes++
				if scenario == "pause during retry" && writes == 1 {
					state.Status = "deferred"
					return nil
				}
				if scenario == "still hooked" || (scenario == "lost write" && writes == 1) {
					return nil
				}
				if scenario == "write error" && writes == 1 {
					return errors.New("write failed")
				}
				state.Status, state.Assignee = status, ""
				return nil
			}
			err := verifyRollbackUnhook("test-bead", agent, read, write)
			wantErr := scenario == "read error" || scenario == "still hooked" || scenario == "nil read" || scenario == "unknown status"
			if (err != nil) != wantErr {
				t.Fatalf("error = %v, wantErr %t", err, wantErr)
			}
			if scenario == "still hooked" && writes != unhookVerifyAttempts {
				t.Fatalf("writes = %d", writes)
			}
			if scenario == "pause" && state.Status != "blocked" {
				t.Fatal("operator pause reopened")
			}
			if scenario == "pause during retry" && (state.Status != "deferred" || state.Assignee != "") {
				t.Fatal("retry lost operator pause")
			}
			if (scenario == "closed" || scenario == "reassigned" || scenario == "unknown status") && writes != 0 {
				t.Fatal("changed another owner or terminal work")
			}
		})
	}
}
