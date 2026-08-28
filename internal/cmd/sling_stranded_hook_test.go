package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

// Regression tests for hq-a0f, exercising the FAILING rollback path: a dispatch
// whose session never came up, where the unhook that is supposed to release the
// bead does not take effect.
//
// Bounds: this drives rollbackSlingArtifacts against a stubbed `bd` and a
// stubbed bead reader. It proves the rollback refuses to exit quietly when the
// bead is still held. It does NOT exercise tmux, a real Dolt write, or the
// intermittent spawn failure itself.

// newStrandedHookTownRoot builds the minimal workspace the rollback path needs
// and points the process at it. Mirrors the setup in sling_rollback_cleanup_test.go.
func newStrandedHookTownRoot(t *testing.T, bdUnixScript, bdWindowsScript string) string {
	t.Helper()

	townRoot, _ := filepath.EvalSymlinks(t.TempDir())
	for _, dir := range []string{
		filepath.Join(townRoot, "mayor", "rig"),
		filepath.Join(townRoot, "gastown", "mayor", "rig"),
		filepath.Join(townRoot, ".beads"),
		filepath.Join(townRoot, "gastown", ".repo.git"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigs := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"gastown": {
				GitURL:  "git@github.com:test/gastown.git",
				AddedAt: time.Now().Truncate(time.Second),
				BeadsConfig: &config.BeadsConfig{
					Repo:   "local",
					Prefix: "gt-",
				},
			},
		},
	}
	if err := config.SaveRigsConfig(rigsPath, rigs); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir binDir: %v", err)
	}
	writeRollbackCleanupBDStub(t, binDir, bdUnixScript, bdWindowsScript)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(EnvGTRole, "mayor")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevCollectMolecules := collectExistingMoleculesForRollback
	collectExistingMoleculesForRollback = func(*beadInfo) []string { return nil }
	t.Cleanup(func() { collectExistingMoleculesForRollback = prevCollectMolecules })

	return townRoot
}

// stubBeadStatus makes every rollback read of the bead report the given status.
// Returns a counter of how many reads happened.
func stubBeadStatus(t *testing.T, status string) *int {
	t.Helper()
	reads := 0
	prev := getBeadInfoForRollback
	getBeadInfoForRollback = func(string) (*beadInfo, error) {
		reads++
		return &beadInfo{Status: status}, nil
	}
	t.Cleanup(func() { getBeadInfoForRollback = prev })
	return &reads
}

const bdAlwaysOKUnix = "#!/bin/sh\nexit 0\n"
const bdAlwaysOKWindows = "@echo off\r\nexit /b 0\r\n"

// TestRollbackReportsStrandedBeadWhenUnhookDoesNotTakeEffect is the core
// regression for hq-a0f acceptance criterion 2. `bd update` exits 0 but the bead
// keeps reading status=hooked — the shape that leaves work attached to a polecat
// that is about to be torn down. The rollback must say so loudly instead of
// printing a dim warning and returning.
func TestRollbackReportsStrandedBeadWhenUnhookDoesNotTakeEffect(t *testing.T) {
	townRoot := newStrandedHookTownRoot(t, bdAlwaysOKUnix, bdAlwaysOKWindows)
	reads := stubBeadStatus(t, "hooked")

	spawnInfo := &SpawnedPolecatInfo{
		RigName:     "gastown",
		PolecatName: "Toast",
		ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "Toast"),
	}

	out := captureStdout(t, func() {
		rollbackSlingArtifacts(spawnInfo, "gt-abc123", "", "")
	})

	if !strings.Contains(out, "STRANDED WORK") {
		t.Errorf("rollback did not report the stranded bead loudly.\nOutput:\n%s", out)
	}
	if !strings.Contains(out, "gt-abc123") {
		t.Errorf("stranded report does not name the bead.\nOutput:\n%s", out)
	}
	if strings.Contains(out, "Unhooked bead gt-abc123 (verified)") {
		t.Errorf("rollback claimed a verified unhook for a bead still reading hooked.\nOutput:\n%s", out)
	}
	// One read per unhook attempt: the rollback must actually re-check, not
	// trust the write's exit code.
	if *reads < unhookVerifyAttempts {
		t.Errorf("bead status was read %d times, want at least %d (one per retry)", *reads, unhookVerifyAttempts)
	}
}

// TestRollbackReportsStrandedBeadWhenUnhookWriteFails covers the other half:
// the `bd update` itself fails every time (the Dolt-unreachable case).
func TestRollbackReportsStrandedBeadWhenUnhookWriteFails(t *testing.T) {
	townRoot := newStrandedHookTownRoot(t,
		"#!/bin/sh\ncase \"$*\" in *update*) exit 1;; esac\nexit 0\n",
		"@echo off\r\necho %* | findstr /C:\"update\" >nul && exit /b 1\r\nexit /b 0\r\n")
	stubBeadStatus(t, "hooked")

	spawnInfo := &SpawnedPolecatInfo{
		RigName:     "gastown",
		PolecatName: "Toast",
		ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "Toast"),
	}

	out := captureStdout(t, func() {
		rollbackSlingArtifacts(spawnInfo, "gt-abc123", "", "")
	})

	if !strings.Contains(out, "STRANDED WORK") {
		t.Errorf("rollback did not report the stranded bead loudly.\nOutput:\n%s", out)
	}
	if !strings.Contains(out, "bd update gt-abc123 --status=open") {
		t.Errorf("stranded report does not give a recovery command.\nOutput:\n%s", out)
	}
}

// TestRollbackConfirmsVerifiedUnhookOnTheHappyPath guards against the check
// becoming noise: when the bead really is released, the rollback says so once
// and does not cry stranded.
func TestRollbackConfirmsVerifiedUnhookOnTheHappyPath(t *testing.T) {
	townRoot := newStrandedHookTownRoot(t, bdAlwaysOKUnix, bdAlwaysOKWindows)
	stubBeadStatus(t, "open")

	spawnInfo := &SpawnedPolecatInfo{
		RigName:     "gastown",
		PolecatName: "Toast",
		ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "Toast"),
	}

	out := captureStdout(t, func() {
		rollbackSlingArtifacts(spawnInfo, "gt-abc123", "", "")
	})

	if strings.Contains(out, "STRANDED WORK") {
		t.Errorf("rollback reported stranded work for a released bead.\nOutput:\n%s", out)
	}
	if !strings.Contains(out, "Unhooked bead gt-abc123 (verified)") {
		t.Errorf("rollback did not confirm the verified unhook.\nOutput:\n%s", out)
	}
}
