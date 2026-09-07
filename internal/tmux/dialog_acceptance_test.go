package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestAcceptWorkspaceTrustDialog_NoDialog verifies that when no trust dialog
// is present (agent prompt visible), the function returns quickly without error.
func TestAcceptWorkspaceTrustDialog_NoDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-trust-nodlg-" + t.Name()

	_ = tm.KillSession(sessionName)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Session starts with a shell prompt containing ">", "$", or "%"
	// The polling loop should exit early when it sees the prompt.
	start := time.Now()
	err := tm.AcceptWorkspaceTrustDialog(sessionName)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AcceptWorkspaceTrustDialog: %v", err)
	}

	// Should complete well before the 8s timeout since prompt is visible
	if elapsed > 6*time.Second {
		t.Errorf("took %v, expected early exit (< 6s)", elapsed)
	}
}

// TestAcceptWorkspaceTrustDialog_DetectsDialog verifies that when trust dialog
// text appears in the pane, it is detected and accepted (Enter key sent).
func TestAcceptWorkspaceTrustDialog_DetectsDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-trust-dlg-" + t.Name()

	_ = tm.KillSession(sessionName)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Simulate the trust dialog by echoing its text into the pane
	if err := tm.SendKeys(sessionName, "echo 'Quick safety check - do you trust this folder?'"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	// Give the echo a moment to execute
	time.Sleep(300 * time.Millisecond)

	err := tm.AcceptWorkspaceTrustDialog(sessionName)
	if err != nil {
		t.Fatalf("AcceptWorkspaceTrustDialog: %v", err)
	}

	// Verify that Enter was sent (we can't easily verify the exact keypress,
	// but the function should return without error after detecting the dialog)
}

// TestAcceptWorkspaceTrustDialog_DetectsCodexDialog verifies that Codex's
// workspace trust prompt is treated as a trust dialog instead of an agent prompt.
func TestAcceptWorkspaceTrustDialog_DetectsCodexDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-trust-codex-" + t.Name()

	_ = tm.KillSession(sessionName)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	if err := tm.SendKeys(sessionName, "echo '> You are in /tmp/demo'; echo 'Do you trust the contents of this directory?'"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	if err := tm.AcceptWorkspaceTrustDialog(sessionName); err != nil {
		t.Fatalf("AcceptWorkspaceTrustDialog: %v", err)
	}
}

// TestAcceptBypassPermissionsWarning_NoDialog verifies that when no bypass
// permissions dialog is present, the function returns quickly without error.
func TestAcceptBypassPermissionsWarning_NoDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-bypass-nodlg-" + t.Name()

	_ = tm.KillSession(sessionName)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	start := time.Now()
	err := tm.AcceptBypassPermissionsWarning(sessionName)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AcceptBypassPermissionsWarning: %v", err)
	}

	if elapsed > 6*time.Second {
		t.Errorf("took %v, expected early exit (< 6s)", elapsed)
	}
}

// TestAcceptBypassPermissionsWarning_DetectsDialog verifies that when bypass
// permissions dialog text appears in the pane, it is detected and accepted.
func TestAcceptBypassPermissionsWarning_DetectsDialog(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-bypass-dlg-" + t.Name()

	_ = tm.KillSession(sessionName)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Simulate the bypass permissions dialog
	if err := tm.SendKeys(sessionName, "echo 'Bypass Permissions mode is enabled'"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	err := tm.AcceptBypassPermissionsWarning(sessionName)
	if err != nil {
		t.Fatalf("AcceptBypassPermissionsWarning: %v", err)
	}
}

// TestAcceptStartupDialogs_NoDialogs verifies the combined function returns
// quickly when no dialogs are present.
func TestAcceptStartupDialogs_NoDialogs(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-startup-nodlg-" + t.Name()

	_ = tm.KillSession(sessionName)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	start := time.Now()
	err := tm.AcceptStartupDialogs(sessionName)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AcceptStartupDialogs: %v", err)
	}

	// Both dialog checks should early-exit when prompt is visible
	if elapsed > 12*time.Second {
		t.Errorf("took %v, expected faster completion", elapsed)
	}
}

// TestAcceptWorkspaceTrustDialog_InvalidSession verifies error handling
// when the session doesn't exist.
func TestAcceptWorkspaceTrustDialog_InvalidSession(t *testing.T) {
	tm := newTestTmux(t)

	// A vanished pane must be reported as an observation failure.
	err := tm.AcceptWorkspaceTrustDialog("gt-nonexistent-session-xyz")
	if err == nil {
		t.Fatal("expected observation error for nonexistent session")
	}
}

// TestContainsPromptIndicator verifies the prompt detection helper
// recognizes various shell and agent prompt patterns.
func TestContainsPromptIndicator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"claude prompt", "Hello! How can I help?\n>", true},
		{"codex prompt", "Ready\n› ", true},
		{"bash prompt", "user@host:~$", true},
		{"zsh prompt", "╰─❯", true},
		{"root prompt", "root@host:~#", true},
		{"csh prompt", "host%", true},
		{"dialog text only", "Quick safety check\nDo you trust this folder?", false},
		{"empty", "", false},
		{"whitespace only", "   \n  \n  ", false},
		{"bypass dialog", "Bypass Permissions mode\n1. No\n2. Yes, I accept", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsPromptIndicator(tt.content)
			if got != tt.want {
				t.Errorf("containsPromptIndicator(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestContainsWorkspaceTrustDialog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"claude trust prompt", "Quick safety check\nDo you trust this folder?", true},
		{"codex trust prompt", "> You are in /tmp/demo\nDo you trust the contents of this directory?", true},
		{"bypass dialog", "Bypass Permissions mode\n1. No\n2. Yes, I accept", false},
		{"shell prompt", "user@host:~$", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsWorkspaceTrustDialog(tt.content)
			if got != tt.want {
				t.Errorf("containsWorkspaceTrustDialog(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestContainsBlockingStartupDialog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		content     string
		wantBlocked bool
		wantName    string
	}{
		{
			name: "codex update modal",
			content: `Update available! 0.137.0 -> 0.138.0
Update now
Skip
Skip until next version`,
			wantBlocked: true,
			wantName:    "codex update prompt",
		},
		{
			name:        "codex trust modal",
			content:     "> You are in /tmp/demo\nDo you trust the contents of this directory?",
			wantBlocked: true,
			wantName:    "workspace trust prompt",
		},
		{
			name:        "bypass modal",
			content:     "Bypass Permissions mode\n1. No\n2. Yes, I accept",
			wantBlocked: true,
			wantName:    "bypass permissions prompt",
		},
		{
			name:        "ready prompt",
			content:     "› ",
			wantBlocked: false,
		},
		{
			name: "stale bypass dialog before codex prompt",
			content: `Bypass Permissions mode
1. No
2. Yes, I accept
› `,
			wantBlocked: false,
		},
		{
			name: "stale bypass dialog before prompt and status",
			content: `Bypass Permissions mode
1. No
2. Yes, I accept
›
session ready`,
			wantBlocked: false,
		},
		{
			name: "stale trust dialog before shell prompt",
			content: `Quick safety check
Do you trust this folder?
user@host:~$`,
			wantBlocked: false,
		},
		{
			name: "old shell prompt before current bypass dialog",
			content: `user@host:~$
Bypass Permissions mode
1. No
2. Yes, I accept`,
			wantBlocked: true,
			wantName:    "bypass permissions prompt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotBlocked := containsBlockingStartupDialog(tt.content)
			if gotBlocked != tt.wantBlocked {
				t.Fatalf("blocked = %v, want %v", gotBlocked, tt.wantBlocked)
			}
			if gotName != tt.wantName {
				t.Fatalf("name = %q, want %q", gotName, tt.wantName)
			}
		})
	}
}

// TestDismissStartupDialogsBlind_SendsKeys verifies that the blind dismiss
// sends keys without error on a valid session (no screen-scraping).
func TestDismissStartupDialogsBlind_SendsKeys(t *testing.T) {
	tm := newTestTmux(t)
	sessionName := "gt-test-blind-dismiss-" + t.Name()

	_ = tm.KillSession(sessionName)
	if err := tm.NewSession(sessionName, ""); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer func() { _ = tm.KillSession(sessionName) }()

	// Should complete quickly — no polling, no CapturePane
	start := time.Now()
	err := tm.DismissStartupDialogsBlind(sessionName)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("DismissStartupDialogsBlind: %v", err)
	}

	// Should take ~700ms (500ms + 200ms sleeps) — not the 8s+ dialog poll timeout
	if elapsed > 3*time.Second {
		t.Errorf("took %v, expected ~700ms (no polling)", elapsed)
	}
}

// TestDismissStartupDialogsBlind_InvalidSession verifies error handling
// when the session doesn't exist.
func TestDismissStartupDialogsBlind_InvalidSession(t *testing.T) {
	tm := newTestTmux(t)

	err := tm.DismissStartupDialogsBlind("gt-nonexistent-session-blind-xyz")
	// Should return an error since the session doesn't exist
	if err == nil {
		t.Error("expected error for nonexistent session, got nil")
	}
}

func TestWorkspaceTrustKeys(t *testing.T) {
	for _, tt := range []struct {
		name, screen string
		keys         []string
	}{
		{"claude reversed default", "Quick safety check\n❯ No, exit\n  Yes, I trust this folder", []string{"Down", "Enter"}},
		{"claude old default", "Quick safety check\n❯ 1. Yes, I trust this folder\n  2. No, exit", []string{"Enter"}},
		{"codex decline selected", "Do you trust the contents of this directory?\n  1. Yes, continue\n› 2. No, quit", []string{"Up", "Enter"}},
		{"codex accept selected", "Do you trust the contents of this directory?\n› 1. Yes, continue\n  2. No, quit", []string{"Enter"}},
		{"unknown selection", "Quick safety check\nNo, exit\nYes, I trust this folder", nil},
		{"unknown acceptance", "Quick safety check\n❯ No, exit\nMaybe", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := workspaceTrustKeys(tt.screen)
			if tt.keys == nil {
				if err == nil {
					t.Fatal("unknown menu accepted")
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.keys) {
				t.Fatalf("keys=%v err=%v, want %v", got, err, tt.keys)
			}
		})
	}
}

func TestStartupDialogHistoryWithPopulatedComposer(t *testing.T) {
	for _, prompt := range []string{"› Run gt prime", "❯ Implement the assignment"} {
		screen := "Do you trust the contents of this directory?\n› 1. Yes, continue\n  2. No, quit\n" + prompt + "\nWorking (esc to interrupt)"
		if name, blocked := containsBlockingStartupDialog(screen); blocked {
			t.Fatalf("historical dialog classified active: %s", name)
		}
	}
	for _, screen := range []string{"Quick safety check\n❯ No, exit\nYes, I trust this folder", "Do you trust the contents of this directory?\n› 1. Yes, continue\n2. No, quit"} {
		if _, blocked := containsBlockingStartupDialog(screen); !blocked {
			t.Fatal("menu selection mistaken for composer")
		}
	}
}

// Exercise the actual tmux key delivery against a delayed, raw-mode TUI. A
// bare Enter selects No and exits, reproducing the observed Claude failure.
func TestAcceptWorkspaceTrustDialogChangedDefaultLive(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	tm := newTestTmux(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "dialog.py")
	report := filepath.Join(dir, "keys")
	source := `import os, sys, time, tty
 tty.setraw(sys.stdin.fileno())
 time.sleep(9)
 print("\033[2J\033[HQuick safety check\r\n\r\n❯ No, exit\r\n  Yes, I trust this folder\r\nEnter to confirm", flush=True)
 keys = b""
 while not keys.endswith(b"\r"):
  time.sleep(0.2)
  event = os.read(sys.stdin.fileno(), 4096)
  if b"\r" in event and len(event) > 1:
   sys.exit(2)
  keys += event
  if keys in (b"\x1b[B", b"\x1bOB"):
   print("\033[2J\033[HQuick safety check\r\n  No, exit\r\n❯ Yes, I trust this folder\r\nEnter to confirm", flush=True)
 open(sys.argv[1], "w").write(keys.hex())
 if keys not in (b"\x1b[B\r", b"\x1bOB\r"):
  sys.exit(1)
 print("\033[2J\033[H❯ ", flush=True)
 time.sleep(30)
`
	// Python top-level statements must be unindented.
	source = strings.ReplaceAll(source, "\n ", "\n")
	if err := os.WriteFile(script, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	session := "gt-test-trust-reversed"
	if err := tm.NewSessionWithCommand(session, dir, "python3 "+script+" "+report); err != nil {
		t.Fatal(err)
	}
	defer tm.KillSession(session)
	if err := tm.AcceptStartupDialogs(session); err != nil {
		t.Fatal(err)
	}
	if err := tm.CheckStartupBlocked(session); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "1b5b420d" && string(data) != "1b4f420d" {
		t.Fatalf("wrong keys: %s", data)
	}
}

func TestCheckStartupBlockedMissingPane(t *testing.T) {
	tm := newTestTmux(t)
	err := tm.CheckStartupBlocked("gt-missing-startup")
	if err == nil || !strings.Contains(err.Error(), "startup observation failed") || strings.Contains(err.Error(), "dialog still visible") {
		t.Fatalf("wrong classification: %v", err)
	}
}
