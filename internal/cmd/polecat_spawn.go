// Package cmd provides polecat spawning utilities for gt sling.
package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

const minPolecatDirsPerRig = 30

// SpawnedPolecatInfo contains info about a spawned polecat session.
type SpawnedPolecatInfo struct {
	RigName     string // Rig name (e.g., "gastown")
	PolecatName string // Polecat name (e.g., "Toast")
	ClonePath   string // Path to polecat's git worktree
	SessionName string // Tmux session name (e.g., "gt-gastown-p-Toast")
	Pane        string // Tmux pane ID (empty until StartSession is called)
	BaseBranch  string // Effective base branch (e.g., "main", "integration/epic-id")
	Branch      string // Git branch name (for cleanup on rollback)

	// Internal fields for deferred session start
	account string
	agent   string
}

// AgentID returns the agent identifier (e.g., "gastown/polecats/Toast")
func (s *SpawnedPolecatInfo) AgentID() string {
	return fmt.Sprintf("%s/polecats/%s", s.RigName, s.PolecatName)
}

// SessionStarted returns true if the tmux session has been started.
func (s *SpawnedPolecatInfo) SessionStarted() bool {
	return s.Pane != ""
}

// SlingSpawnOptions contains options for spawning a polecat via sling.
type SlingSpawnOptions struct {
	TownRoot      string // Gas Town workspace root; falls back to cwd when empty
	Force         bool   // Force spawn even if polecat has uncommitted work
	Account       string // Claude Code account handle to use
	Create        bool   // Create polecat if it doesn't exist (currently always true for sling)
	HookBead      string // Bead ID to set as hook_bead at spawn time (atomic assignment)
	Agent         string // Agent override for this spawn (e.g., "gemini", "codex", "claude-haiku")
	BaseBranch    string // Override base branch for polecat worktree (e.g., "develop", "release/v2")
	ResumeBranch  string // Resume an existing branch (e.g. PR head) instead of creating polecat/<name>/<bead>+<ts>
	SkipAdmission bool   // Caller already holds a polecat admission reservation

	// PolecatName pins the spawn to one specific polecat. When set, the sling
	// targets that polecat and only that polecat: no idle-polecat reuse, no
	// fresh allocation under a different name. If the named polecat cannot take
	// the work, the sling fails loudly naming it rather than silently
	// reassigning to whichever polecat happens to be free (hq-s1t).
	PolecatName string
}

func effectivePolecatDirCap(configured int) int {
	if configured < minPolecatDirsPerRig {
		return minPolecatDirsPerRig
	}
	return configured
}

func reclaimBrokenIdlePolecatForSling(polecatMgr *polecat.Manager) (bool, error) {
	polecats, err := polecatMgr.List()
	if err != nil {
		return false, err
	}

	for _, candidate := range polecats {
		if candidate == nil || candidate.State != polecat.StateIdle || candidate.Issue != "" {
			continue
		}
		verifyErr := verifyWorktreeExists(candidate.ClonePath)
		if verifyErr == nil || !polecat.IsStructuralWorktreeError(verifyErr) {
			continue
		}

		fmt.Printf("  Reclaiming broken idle polecat %s before allocation: %v\n", candidate.Name, verifyErr)
		if err := polecatMgr.ReclaimBrokenIdlePolecat(candidate.Name); err != nil {
			fmt.Printf("  Broken idle polecat %s was not safe to reclaim: %v\n", candidate.Name, err)
			continue
		}
		fmt.Printf("  %s Broken idle polecat %s reclaimed before assigning new work\n", style.Bold.Render("✓"), candidate.Name)
		return true, nil
	}

	return false, nil
}

// resolveSpawnBaseBranch determines the base branch for a polecat worktree.
// ResumeBranch (gh#3602) takes precedence: when resuming an existing branch we
// must not start from main or auto-detect an integration branch.
func resolveSpawnBaseBranch(r *rig.Rig, opts SlingSpawnOptions) string {
	baseBranch := opts.BaseBranch
	if opts.ResumeBranch != "" {
		return baseBranch
	}
	if baseBranch == "" && opts.HookBead != "" {
		// Auto-detect: check if the hooked bead's parent epic has an integration branch
		settingsPath := filepath.Join(r.Path, "settings", "config.json")
		polecatIntegrationEnabled := true
		if settings, err := config.LoadRigSettings(settingsPath); err == nil && settings.MergeQueue != nil {
			polecatIntegrationEnabled = settings.MergeQueue.IsPolecatIntegrationEnabled()
		}
		if polecatIntegrationEnabled {
			repoGit, repoErr := getRigGit(r.Path)
			if repoErr == nil {
				bd := beads.New(r.Path)
				detected, detectErr := beads.DetectIntegrationBranch(bd, repoGit, opts.HookBead)
				if detectErr == nil && detected != "" {
					baseBranch = "origin/" + detected
					fmt.Printf("  Auto-detected integration branch: %s\n", detected)
				}
			}
		}
	}
	if baseBranch != "" && !strings.HasPrefix(baseBranch, "origin/") {
		baseBranch = "origin/" + baseBranch
	}
	return baseBranch
}

// effectiveSlingBranch strips the origin/ prefix (the formula prepends it) and
// falls back to the rig default branch.
func effectiveSlingBranch(r *rig.Rig, baseBranch string, opts SlingSpawnOptions) string {
	if opts.ResumeBranch != "" {
		return opts.ResumeBranch
	}
	effective := strings.TrimPrefix(baseBranch, "origin/")
	if effective == "" {
		effective = r.DefaultBranch()
	}
	return effective
}

// reuseNamedPolecatForSling prepares one specific, named polecat for a sling.
//
// This is the path taken when the target names a polecat (rig/polecats/<name>
// or the rig/<name> shorthand) that has no live tmux pane. It reuses that exact
// polecat and starts its session, or fails naming it. It must never fall back to
// FindIdlePolecat or AllocateAndAdd: silently handing the work to a different
// polecat breaks role separation while reporting success (hq-s1t).
func reuseNamedPolecatForSling(rigName, polecatName string, r *rig.Rig, polecatMgr *polecat.Manager, t *tmux.Tmux, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
	baseBranch := resolveSpawnBaseBranch(r, opts)
	addOpts := polecat.AddOptions{
		HookBead:     opts.HookBead,
		BaseBranch:   baseBranch,
		ResumeBranch: opts.ResumeBranch,
	}

	if _, err := polecatMgr.ReuseIdlePolecat(polecatName, addOpts); err != nil {
		if errors.Is(err, polecat.ErrPolecatNotFound) {
			return nil, fmt.Errorf("polecat %s/%s does not exist\n"+
				"Sling will not reassign named work to a different polecat.\n"+
				"See existing polecats: gt polecat list %s\n"+
				"Spawn a fresh one instead:  gt sling <bead> %s",
				rigName, polecatName, rigName, rigName)
		}
		return nil, fmt.Errorf("polecat %s/%s cannot take this work: %w\n"+
			"Sling will not reassign named work to a different polecat.\n"+
			"Spawn a fresh one instead: gt sling <bead> %s",
			rigName, polecatName, err, rigName)
	}

	polecatObj, err := polecatMgr.Get(polecatName)
	if err != nil {
		return nil, fmt.Errorf("getting polecat %s/%s after reuse: %w", rigName, polecatName, err)
	}
	if err := verifyWorktreeExists(polecatObj.ClonePath); err != nil {
		return nil, fmt.Errorf("worktree verification failed for %s/%s: %w", rigName, polecatName, err)
	}

	polecatSessMgr := polecat.NewSessionManager(t, r)
	fmt.Printf("%s Polecat %s reused (session start deferred)\n", style.Bold.Render("\u2713"), polecatName)
	_ = events.LogFeed(events.TypeSpawn, "gt", events.SpawnPayload(rigName, polecatName))

	return &SpawnedPolecatInfo{
		RigName:     rigName,
		PolecatName: polecatName,
		ClonePath:   polecatObj.ClonePath,
		SessionName: polecatSessMgr.SessionName(polecatName),
		Pane:        "",
		BaseBranch:  effectiveSlingBranch(r, baseBranch, opts),
		Branch:      polecatObj.Branch,
		account:     opts.Account,
		agent:       opts.Agent,
	}, nil
}

// SpawnPolecatForSling creates a fresh polecat and optionally starts its session.
// This is used by gt sling when the target is a rig name.
// The caller (sling) handles hook attachment and nudging.
func SpawnPolecatForSling(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
	// Find workspace
	townRoot := opts.TownRoot
	if townRoot == "" {
		var err error
		townRoot, err = workspace.FindFromCwdOrError()
		if err != nil {
			return nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
		}
	}

	// Load rig config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return nil, fmt.Errorf("rig '%s' not found", rigName)
	}

	// Get polecat manager (with tmux for session-aware allocation)
	polecatGit := git.NewGit(r.Path)
	t := tmux.NewTmux()
	polecatMgr := polecat.NewManager(r, polecatGit, t)

	// Pre-spawn Dolt health check (gt-94llt7): verify Dolt is reachable before
	// allocating a polecat. Prevents orphaned polecats when Dolt is down.
	if err := polecatMgr.CheckDoltHealth(); err != nil {
		return nil, fmt.Errorf("pre-spawn health check failed: %w", err)
	}

	// Pre-spawn admission control (gt-1obzke): verify Dolt server has connection
	// capacity before spawning. Prevents connection storms during mass sling.
	if err := polecatMgr.CheckDoltServerCapacity(); err != nil {
		return nil, fmt.Errorf("admission control: %w", err)
	}

	if blocked, reason := IsRigParkedOrDocked(townRoot, rigName); blocked {
		undoCmd := "gt rig unpark"
		if reason == "docked" {
			undoCmd = "gt rig undock"
		}
		return nil, fmt.Errorf("cannot sling to %s rig %q\n%s %s", reason, rigName, undoCmd, rigName)
	}

	var admission *polecatAdmissionHandle
	if !opts.SkipAdmission {
		admission, _, err = acquirePolecatAdmissionFn(townRoot, rigName, opts.HookBead, "spawn-or-reuse")
		if err != nil {
			return nil, err
		}
		defer admission.Release()
	}

	// Per-bead respawn circuit breaker (clown show #22):
	// Track how many times this bead has been slung. Block after N attempts
	// to prevent witness→deacon→sling feedback loops.
	if opts.HookBead != "" && !opts.Force {
		if witness.ShouldBlockRespawn(townRoot, opts.HookBead) {
			maxRespawns := config.LoadOperationalConfig(townRoot).GetWitnessConfig().MaxBeadRespawnsV()
			return nil, fmt.Errorf("respawn limit reached for %s (%d attempts). "+
				"This bead keeps failing — investigate before re-dispatching.\n"+
				"Override: gt sling %s %s --force\n"+
				"Reset:    gt sling respawn-reset %s",
				opts.HookBead, maxRespawns,
				opts.HookBead, rigName, opts.HookBead)
		}
		witness.RecordBeadRespawn(townRoot, opts.HookBead)
	}

	// Named target: this sling asked for one specific polecat. Reuse that one or
	// fail naming it — never silently reassign to whichever polecat is free.
	if opts.PolecatName != "" {
		return reuseNamedPolecatForSling(rigName, opts.PolecatName, r, polecatMgr, t, opts)
	}

	if reclaimed, err := reclaimBrokenIdlePolecatForSling(polecatMgr); err != nil {
		style.PrintWarning("could not reclaim broken idle polecat before allocation: %v", err)
	} else if reclaimed {
		fmt.Println("  Allocating fresh polecat after reclaiming broken idle sandbox...")
	}

	// Persistent polecat model (gt-4ac): try to reuse an idle polecat first.
	// Idle polecats have completed their work but kept their sandbox (worktree).
	// Reusing avoids the overhead of creating a new worktree.
	idlePolecat, findErr := polecatMgr.FindIdlePolecat()
	if findErr == nil && idlePolecat != nil {
		polecatName := idlePolecat.Name
		fmt.Printf("Reusing idle polecat: %s\n", polecatName)

		baseBranch := resolveSpawnBaseBranch(r, opts)

		// Reuse the idle polecat with branch-only operations (no worktree add/remove).
		// Phase 3 of persistent-polecat-pool: eliminates ~5s worktree creation overhead.
		// If reuse is unsafe or fails, allocate a new polecat instead of repairing
		// this worktree destructively.
		addOpts := polecat.AddOptions{
			HookBead:     opts.HookBead,
			BaseBranch:   baseBranch,
			ResumeBranch: opts.ResumeBranch,
		}
		reuseOK := false
		if _, err := polecatMgr.ReuseIdlePolecat(polecatName, addOpts); err != nil {
			if errors.Is(err, polecat.ErrPolecatNeedsRecovery) {
				fmt.Printf("  Idle polecat %s needs recovery before reuse: %v; allocating new...\n", polecatName, err)
			} else {
				fmt.Printf("  Branch-only reuse failed for idle polecat %s: %v; allocating new...\n", polecatName, err)
			}
		} else {
			reuseOK = true
		}

		if reuseOK {
			polecatObj, err := polecatMgr.Get(polecatName)
			if err != nil {
				return nil, fmt.Errorf("getting idle polecat after reuse: %w", err)
			}
			if err := verifyWorktreeExists(polecatObj.ClonePath); err != nil {
				return nil, fmt.Errorf("worktree verification failed for reused %s: %w", polecatName, err)
			}

			polecatSessMgr := polecat.NewSessionManager(t, r)
			sessionName := polecatSessMgr.SessionName(polecatName)

			fmt.Printf("%s Polecat %s reused (idle → working, session start deferred)\n", style.Bold.Render("✓"), polecatName)
			_ = events.LogFeed(events.TypeSpawn, "gt", events.SpawnPayload(rigName, polecatName))

			effectiveBranch := effectiveSlingBranch(r, baseBranch, opts)

			return &SpawnedPolecatInfo{
				RigName:     rigName,
				PolecatName: polecatName,
				ClonePath:   polecatObj.ClonePath,
				SessionName: sessionName,
				Pane:        "",
				BaseBranch:  effectiveBranch,
				Branch:      polecatObj.Branch,
				account:     opts.Account,
				agent:       opts.Agent,
			}, nil
		}
	}

	// Per-rig directory cap: prevent unbounded worktree accumulation, but only
	// after trying safe reuse. A reusable preserved polecat should not be blocked
	// just because the rig is already at the directory cap.
	maxPolecatDirsPerRig := effectivePolecatDirCap(r.GetIntConfig("max_polecats"))
	rigPolecatDir := filepath.Join(townRoot, rigName, "polecats")
	if entries, err := os.ReadDir(rigPolecatDir); err == nil {
		dirCount := 0
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				dirCount++
			}
		}
		if dirCount >= maxPolecatDirsPerRig {
			return nil, fmt.Errorf("rig %s has %d polecat directories (max %d). "+
				"Resolve recovery-needed polecats before allocating more slots: gt polecat list %s",
				rigName, dirCount, maxPolecatDirsPerRig, rigName)
		}
	}

	// Determine base branch for polecat worktree.
	baseBranch := resolveSpawnBaseBranch(r, opts)

	// Build add options with hook_bead set atomically at spawn time
	addOpts := polecat.AddOptions{
		HookBead:     opts.HookBead,
		BaseBranch:   baseBranch,
		ResumeBranch: opts.ResumeBranch,
	}

	// No idle polecat available — allocate and create atomically (GH#2215).
	// AllocateAndAdd holds the pool lock through directory creation, preventing
	// concurrent processes from allocating the same name.
	polecatName, _, err := polecatMgr.AllocateAndAdd(addOpts)
	if err != nil {
		return nil, fmt.Errorf("allocating and creating polecat: %w", err)
	}
	fmt.Printf("Created polecat: %s\n", polecatName)

	// Get polecat object for path info
	polecatObj, err := polecatMgr.Get(polecatName)
	if err != nil {
		return nil, fmt.Errorf("getting polecat after creation: %w", err)
	}

	// Verify worktree was actually created (fixes #1070)
	// The identity bead may exist but worktree creation can fail silently
	if err := verifyWorktreeExists(polecatObj.ClonePath); err != nil {
		// Clean up the partial state before returning error
		_ = polecatMgr.Remove(polecatName, true) // force=true to clean up partial state
		return nil, fmt.Errorf("worktree verification failed for %s: %w\nHint: try 'gt polecat nuke %s/%s --force' to clean up",
			polecatName, err, rigName, polecatName)
	}

	// Get session manager for session name (session start is deferred)
	polecatSessMgr := polecat.NewSessionManager(t, r)
	sessionName := polecatSessMgr.SessionName(polecatName)

	fmt.Printf("%s Polecat %s spawned (session start deferred)\n", style.Bold.Render("✓"), polecatName)

	// Log spawn event to activity feed
	_ = events.LogFeed(events.TypeSpawn, "gt", events.SpawnPayload(rigName, polecatName))

	// Compute effective base branch (strip origin/ prefix since formula prepends it)
	effectiveBranch := effectiveSlingBranch(r, baseBranch, opts)

	return &SpawnedPolecatInfo{
		RigName:     rigName,
		PolecatName: polecatName,
		ClonePath:   polecatObj.ClonePath,
		SessionName: sessionName,
		Pane:        "", // Empty until StartSession is called
		BaseBranch:  effectiveBranch,
		Branch:      polecatObj.Branch,
		account:     opts.Account,
		agent:       opts.Agent,
	}, nil
}

// StartSession starts the tmux session for a spawned polecat.
// This is called after the molecule/bead is attached, so the polecat
// sees its work when gt prime runs on session start.
// Returns the pane ID after session start.
func (s *SpawnedPolecatInfo) StartSession() (string, error) {
	if s.SessionStarted() {
		return s.Pane, nil
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rig config
	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(s.RigName)
	if err != nil {
		return "", fmt.Errorf("rig '%s' not found", s.RigName)
	}

	// Resolve account
	accountsPath := constants.MayorAccountsPath(townRoot)
	claudeConfigDir, _, err := config.ResolveAccountConfigDir(accountsPath, s.account)
	if err != nil {
		return "", fmt.Errorf("resolving account: %w", err)
	}

	// Start session
	t := tmux.NewTmux()
	polecatSessMgr := polecat.NewSessionManager(t, r)

	fmt.Printf("Starting session for %s/%s...\n", s.RigName, s.PolecatName)
	startOpts := polecat.SessionStartOptions{
		RuntimeConfigDir: claudeConfigDir,
		Agent:            s.agent,
	}
	if err := polecatSessMgr.Start(s.PolecatName, startOpts); err != nil {
		return "", fmt.Errorf("starting session: %w", err)
	}

	// Wait for runtime to be fully ready before returning.
	// When an agent override is specified (e.g., --agent codex), resolve the runtime
	// config from the override so WaitForRuntimeReady uses the correct readiness
	// strategy (delay-based for Codex vs prompt-polling for Claude). Without this,
	// ResolveRoleAgentConfig returns the default agent (Claude) and polls for "❯ "
	// in a Codex session, always timing out after 30 seconds (gt-1j3m).
	spawnTownRoot := filepath.Dir(r.Path)
	var runtimeConfig *config.RuntimeConfig
	if s.agent != "" {
		rc, _, err := config.ResolveAgentConfigWithOverride(spawnTownRoot, r.Path, s.agent)
		if err != nil {
			style.PrintWarning("resolving agent config for %s: %v (using default)", s.agent, err)
			runtimeConfig = config.ResolveRoleAgentConfig("polecat", spawnTownRoot, r.Path)
		} else {
			runtimeConfig = rc
		}
	} else {
		runtimeConfig = config.ResolveRoleAgentConfig("polecat", spawnTownRoot, r.Path)
	}
	if err := t.WaitForRuntimeReady(s.SessionName, runtimeConfig, 30*time.Second); err != nil {
		style.PrintWarning("runtime may not be fully ready: %v", err)
	}

	// Prove the session is actually live before claiming the spawn succeeded (hq-a0f).
	//
	// Every check up to this point is one-shot and early: tmux probes pane_dead
	// twice within 250ms of creating the session, and SessionManager.Start's
	// HasSession/CheckSessionHealth pair runs before the readiness wait above.
	// A runtime that starts and then exits passes all of them, and the
	// WaitForRuntimeReady failure right above is warn-only — so StartSession used
	// to return a pane for a session with no agent in it. The dispatcher then
	// printed success over work hooked to an agent that does not exist.
	//
	// Run this BEFORE the agent-state writes below so a dead session is never
	// recorded as "working".
	if err := verifySessionLive(t, s.SessionName, spawnLivenessTimeout, spawnLivenessInterval); err != nil {
		if errors.Is(err, errSessionUnverifiable) {
			// tmux never answered. Nothing was observed, so this is not evidence
			// the agent is dead — warn with the bounds stated and continue.
			style.PrintWarning("could not verify session liveness for %s (continuing): %v", s.SessionName, err)
		} else {
			_ = t.KillSessionWithProcesses(s.SessionName)
			return "", fmt.Errorf("verifying session for %s/%s: %w", s.RigName, s.PolecatName, err)
		}
	}

	// Update agent state with retry logic (gt-94llt7: fail-safe Dolt writes).
	// Note: warn-only, not fail-hard. The tmux session is already started above,
	// so returning an error here would leave an orphaned session with no cleanup path.
	// The polecat can still function without the agent state update — it only affects
	// monitoring visibility, not correctness. Compare with createAgentBeadWithRetry
	// which fails hard because a polecat without an agent bead is untrackable.
	polecatGit := git.NewGit(r.Path)
	polecatMgr := polecat.NewManager(r, polecatGit, t)
	if err := polecatMgr.SetAgentStateWithRetry(s.PolecatName, "working"); err != nil {
		style.PrintWarning("could not update agent state after retries: %v", err)
	}

	// Update issue status from hooked to in_progress.
	// Also warn-only for the same reason: session is already running.
	if err := polecatMgr.SetState(s.PolecatName, polecat.StateWorking); err != nil {
		style.PrintWarning("could not update issue status to in_progress: %v", err)
	}

	// Get pane — if this fails, the session may have died during startup.
	// Kill the dead session to prevent "session already running" on next attempt (gt-jn40ft).
	pane, err := getSessionPane(s.SessionName)
	if err != nil {
		// Session likely died — clean up the tmux session so it doesn't block re-sling
		_ = t.KillSession(s.SessionName)
		return "", fmt.Errorf("getting pane for %s (session likely died during startup): %w", s.SessionName, err)
	}

	s.Pane = pane
	return pane, nil
}

// IsRigName checks if a target string is a rig name (not a role or path).
// Returns the rig name and true if it's a valid rig.
func IsRigName(target string) (string, bool) {
	// If it contains a slash, it's a path format (rig/role or rig/crew/name)
	if strings.Contains(target, "/") {
		return "", false
	}

	// Check known non-rig role names
	switch strings.ToLower(target) {
	case constants.RoleMayor, "may", constants.RoleDeacon, "dea", constants.RoleCrew, constants.RoleWitness, "wit", constants.RoleRefinery, "ref":
		return "", false
	}

	// Try to load as a rig
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", false
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return "", false
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	_, err = rigMgr.GetRig(target)
	if err != nil {
		return "", false
	}

	return target, true
}

// verifyWorktreeExists checks that a git worktree was actually created at the given path
// and that it is a functional git repository. Returns an error if the worktree is missing,
// has a broken .git reference, or fails basic git validation. (GH#2056)
func verifyWorktreeExists(clonePath string) error {
	return polecat.VerifyWorktreeExists(clonePath)
}
