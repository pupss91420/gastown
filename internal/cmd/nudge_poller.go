package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	nudgePollerIntervalFlag string
	nudgePollerIdleFlag     string
	nudgePollerOwnerToken   string
)

func init() {
	rootCmd.AddCommand(nudgePollerCmd)
	nudgePollerCmd.Flags().StringVar(&nudgePollerOwnerToken, "owner-token", "", "Internal launch ownership token")
	nudgePollerCmd.Flags().StringVar(&nudgePollerIntervalFlag, "interval", nudge.DefaultPollInterval, "Poll interval (e.g., 10s, 30s)")
	nudgePollerCmd.Flags().StringVar(&nudgePollerIdleFlag, "idle-timeout", nudge.DefaultIdleTimeout, "How long to wait for agent idle before skipping")
}

var nudgePollerCmd = &cobra.Command{
	Use:    "nudge-poller <session>",
	Short:  "Background nudge queue poller for non-Claude agents",
	Hidden: true, // Internal command — launched by crew manager, not by users.
	Long: `Polls the nudge queue for a tmux session and drains it when the agent
is idle. This is the background equivalent of Claude's UserPromptSubmit hook
drain — it ensures queued nudges are delivered to agents that lack
turn-boundary hooks (Gemini, Codex, Cursor, etc.).

This command runs as a long-lived background process. It exits when:
  - The target tmux session dies
  - It receives SIGTERM (from StopPoller or session teardown)
  - The poll loop encounters an unrecoverable error

Normally launched automatically by 'gt crew start' for non-Claude agents.
Not intended for direct user invocation.`,
	Args: cobra.ExactArgs(1),
	RunE: runNudgePoller,
}

func runNudgePoller(cmd *cobra.Command, args []string) error {
	sessionName := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("cannot find town root: %w", err)
	}

	pollInterval, err := time.ParseDuration(nudgePollerIntervalFlag)
	if err != nil {
		return fmt.Errorf("invalid --interval: %w", err)
	}

	idleTimeout, err := time.ParseDuration(nudgePollerIdleFlag)
	if err != nil {
		return fmt.Errorf("invalid --idle-timeout: %w", err)
	}

	if pollInterval <= 0 || idleTimeout <= 0 {
		return fmt.Errorf("poll interval and idle timeout must be positive")
	}

	t := tmux.NewTmux()

	// Verify session exists before starting the loop.
	if exists, _ := t.HasSession(sessionName); !exists {
		return fmt.Errorf("session %q not found", sessionName)
	}

	// Resolve nudge options once at startup: if the target agent uses Escape
	// as cancel (e.g., Gemini CLI), skip the Escape keystroke during delivery
	// to avoid canceling in-flight generation. (GH#gt-wasn)
	nudgeOpts := tmux.NudgeOpts{TownRoot: townRoot}
	agentName := ""
	hasPromptDetection := false
	if name, err := t.GetEnvironment(sessionName, "GT_AGENT"); err == nil && name != "" {
		agentName = name
		if preset := config.GetAgentPresetByName(agentName); preset != nil {
			hasPromptDetection = preset.ReadyPromptPrefix != ""
			if preset.EscapeCancelsRequest {
				nudgeOpts.SkipEscape = true
			}
		}
	}

	// Set up signal handling for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	cleanupReady, err := nudge.MarkPollerReady(townRoot, sessionName, nudgePollerOwnerToken)
	if err != nil {
		return fmt.Errorf("publishing poller readiness: %w", err)
	}
	defer cleanupReady()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sigCh:
			return nil // graceful shutdown

		case <-ticker.C:
			// Check if session still exists.
			if exists, _ := t.HasSession(sessionName); !exists {
				return nil // session gone, exit
			}

			if err := pollNudgeQueue(t, townRoot, sessionName, idleTimeout, hasPromptDetection, nudgeOpts); err != nil {
				fmt.Fprintf(os.Stderr, "nudge-poller: %s: %v\n", sessionName, err)
			}
		}
	}
}

func shouldSkipDrainUntilIdle(hasPromptDetection bool, waitErr error) bool {
	return hasPromptDetection && waitErr != nil
}

// nudgePollerTarget is the delivery boundary; tests exercise real queue files
// while replacing only the terminal, without touching live agent sessions.
type nudgePollerTarget interface {
	WaitForIdle(string, time.Duration) error
	NudgeSessionWithOpts(string, string, tmux.NudgeOpts) error
}

func pollNudgeQueue(t nudgePollerTarget, townRoot, sessionName string, idleTimeout time.Duration, hasPromptDetection bool, opts tmux.NudgeOpts) error {
	n, err := nudge.Pending(townRoot, sessionName)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if shouldSkipDrainUntilIdle(hasPromptDetection, t.WaitForIdle(sessionName, idleTimeout)) {
		return nil
	}
	drained, err := nudge.Drain(townRoot, sessionName)
	if err != nil {
		return err
	}
	if len(drained) == 0 {
		return nil
	}
	if err := t.NudgeSessionWithOpts(sessionName, nudge.FormatForInjection(drained), opts); err != nil {
		requeueDrainedNudges(townRoot, sessionName, "nudge-poller", drained)
		return fmt.Errorf("injection: %w", err)
	}
	return nil
}
