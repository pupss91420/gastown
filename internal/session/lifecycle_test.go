package session

import (
	"errors"
	"github.com/steveyegge/gastown/internal/tmux"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
)

func TestStartSession_RequiresSessionID(t *testing.T) {
	_, err := StartSession(nil, SessionConfig{
		WorkDir: "/tmp",
		Role:    "polecat",
	})
	if err == nil {
		t.Fatal("expected error for missing SessionID")
	}
	if err.Error() != "SessionID is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStartSession_RequiresWorkDir(t *testing.T) {
	_, err := StartSession(nil, SessionConfig{
		SessionID: "gt-test",
		Role:      "polecat",
	})
	if err == nil {
		t.Fatal("expected error for missing WorkDir")
	}
	if err.Error() != "WorkDir is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStartSession_RequiresRole(t *testing.T) {
	_, err := StartSession(nil, SessionConfig{
		SessionID: "gt-test",
		WorkDir:   "/tmp",
	})
	if err == nil {
		t.Fatal("expected error for missing Role")
	}
	if err.Error() != "Role is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildPrompt_BeaconOnly(t *testing.T) {
	cfg := SessionConfig{
		Beacon: BeaconConfig{
			Recipient: "boot",
			Sender:    "daemon",
			Topic:     "triage",
		},
	}
	prompt := buildPrompt(cfg)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !contains(prompt, "[GAS TOWN]") {
		t.Errorf("prompt should contain beacon: %s", prompt)
	}
}

func TestBuildPrompt_WithInstructions(t *testing.T) {
	cfg := SessionConfig{
		Beacon: BeaconConfig{
			Recipient: "boot",
			Sender:    "daemon",
			Topic:     "triage",
		},
		Instructions: "Run gt boot triage now.",
	}
	prompt := buildPrompt(cfg)
	if !contains(prompt, "Run gt boot triage now.") {
		t.Errorf("prompt should contain instructions: %s", prompt)
	}
	if !contains(prompt, "[GAS TOWN]") {
		t.Errorf("prompt should contain beacon: %s", prompt)
	}
}

func TestBuildCommand_DefaultAgent(t *testing.T) {
	cfg := SessionConfig{
		Role:     "boot",
		TownRoot: "/tmp/town",
	}
	cmd, err := buildCommand(cfg, "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
}

func TestBuildCommand_WithAgentOverride(t *testing.T) {
	cfg := SessionConfig{
		Role:          "boot",
		TownRoot:      "/tmp/town",
		AgentOverride: "opencode",
	}
	cmd, err := buildCommand(cfg, "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
}

func TestKillExistingSession_NoSession(t *testing.T) {
	// KillExistingSession with nil tmux would panic, but we test the logic
	// by verifying it's callable. Full integration tests need a real tmux.
	// This test verifies the function signature and basic flow.
	t.Skip("requires tmux for integration testing")
}

func TestMapKeysSorted(t *testing.T) {
	got := mapKeysSorted(map[string]string{
		"GT_SESSION": "1",
		"GT_ROLE":    "polecat",
		"GT_RIG":     "alpha",
	})

	want := []string{"GT_RIG", "GT_ROLE", "GT_SESSION"}
	if len(got) != len(want) {
		t.Fatalf("mapKeysSorted() length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mapKeysSorted()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMergeRuntimeLivenessEnv_SetsResolvedAgentAndProcessNames(t *testing.T) {
	env := map[string]string{
		"GT_ROLE": "polecat",
	}
	rc := &config.RuntimeConfig{
		Command:       "claude",
		ResolvedAgent: "claude",
	}

	got := MergeRuntimeLivenessEnv(env, rc)

	if got["GT_AGENT"] != "claude" {
		t.Fatalf("GT_AGENT = %q, want %q", got["GT_AGENT"], "claude")
	}
	if got["GT_PROCESS_NAMES"] != "node,claude" {
		t.Fatalf("GT_PROCESS_NAMES = %q, want %q", got["GT_PROCESS_NAMES"], "node,claude")
	}
}

func TestMergeRuntimeLivenessEnv_RespectsExistingValues(t *testing.T) {
	env := map[string]string{
		"GT_AGENT":         "explicit-agent",
		"GT_PROCESS_NAMES": "custom-bin,custom-agent",
	}
	rc := &config.RuntimeConfig{
		Command:       "bun",
		ResolvedAgent: "wen",
	}

	got := MergeRuntimeLivenessEnv(env, rc)

	if got["GT_AGENT"] != "explicit-agent" {
		t.Fatalf("GT_AGENT = %q, want %q", got["GT_AGENT"], "explicit-agent")
	}
	if got["GT_PROCESS_NAMES"] != "custom-bin,custom-agent" {
		t.Fatalf("GT_PROCESS_NAMES = %q, want %q", got["GT_PROCESS_NAMES"], "custom-bin,custom-agent")
	}
}

func TestMergeRuntimeLivenessEnv_UsesEffectiveAgentForProcessNames(t *testing.T) {
	// When AgentOverride sets GT_AGENT to a different agent than
	// runtimeConfig.ResolvedAgent, process names must be resolved from
	// the effective agent (GT_AGENT), not the workspace-default resolved agent.
	env := map[string]string{
		"GT_AGENT": "codex", // set by AgentEnv from AgentOverride
	}
	rc := &config.RuntimeConfig{
		Command:       "claude",
		ResolvedAgent: "claude", // workspace default, NOT the override
	}

	got := MergeRuntimeLivenessEnv(env, rc)

	if got["GT_AGENT"] != "codex" {
		t.Fatalf("GT_AGENT = %q, want %q", got["GT_AGENT"], "codex")
	}
	if got["GT_PROCESS_NAMES"] != "codex" {
		t.Fatalf("GT_PROCESS_NAMES = %q, want %q (should resolve from effective agent, not runtimeConfig)", got["GT_PROCESS_NAMES"], "codex")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// A stale Claude hook file must not suppress Codex's real startup drain.
func TestStartSessionCodexRegistersNudgePoller(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX tmux stub")
	}
	root := t.TempDir()
	work := filepath.Join(root, "mayor")
	if err := os.MkdirAll(filepath.Join(work, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".claude", "settings.json"), []byte(`{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"gt nudge drain"}]}]}}`), 0644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Chdir(root)
	original := startNudgePoller
	t.Cleanup(func() { startNudgePoller = original })
	calls := 0
	startNudgePoller = func(townRoot, sessionID string) (int, error) {
		calls++
		if townRoot != root || sessionID != "hq-mayor" {
			t.Fatalf("wrong poller target %q %q", townRoot, sessionID)
		}
		return 123, nil
	}
	result, err := StartSession(tmux.NewTmux(), SessionConfig{
		SessionID: "hq-mayor", WorkDir: work, TownRoot: root, Role: "mayor",
		AgentOverride: "codex", Command: "sleep 30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimeConfig.ResolvedAgent != "codex" {
		t.Fatalf("resolved agent: %s", result.RuntimeConfig.ResolvedAgent)
	}
	if calls != 1 {
		t.Fatalf("poller registrations=%d, want 1", calls)
	}
	// A failed session creation must not launch a poller for a nonexistent target.
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err = StartSession(tmux.NewTmux(), SessionConfig{
		SessionID: "hq-mayor", WorkDir: work, TownRoot: root, Role: "mayor",
		AgentOverride: "codex", Command: "sleep 30",
	})
	if err == nil {
		t.Fatal("expected failed session creation")
	}
	if calls != 1 {
		t.Fatal("failed startup registered poller")
	}
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	pollerErr := errors.New("poller launch failed")
	startNudgePoller = func(string, string) (int, error) { return 0, pollerErr }
	_, err = StartSession(tmux.NewTmux(), SessionConfig{
		SessionID: "hq-mayor", WorkDir: work, TownRoot: root, Role: "mayor",
		AgentOverride: "codex", Command: "sleep 30",
	})
	if err != nil {
		t.Fatalf("live mayor reported spawn failure after poller error: %v", err)
	}
}
