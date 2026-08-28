package witness

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// push_failed is a latch: a polecat sets it when a push errors and nothing
// clears it when the work becomes durable anyway — by a later push, a retry, or
// a merge. The escalation it drives tells the mayor "branch not on origin,
// possible work loss", so it has to ask origin first. During the gastown org
// transfer that claim was wrong for every polecat it fired on (hq-wsw).
//
// It must keep firing for work that really is stranded, though. An operator who
// learns PUSH_FAILED means nothing will miss the real one, so the tests below
// pin both directions and the unmeasurable middle.

func pushFailedRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

// pushFailedFixture builds a town-shaped worktree at
// <town>/<rig>/polecats/<name>/<rig> with an origin remote, and returns the
// town root and the worktree path.
func pushFailedFixture(t *testing.T, rigName, polecatName string) (townRoot, clonePath string) {
	t.Helper()
	townRoot = t.TempDir()

	remote := filepath.Join(townRoot, "remote.git")
	pushFailedRun(t, townRoot, "git", "init", "--bare", remote)

	clonePath = filepath.Join(townRoot, rigName, "polecats", polecatName, rigName)
	if err := os.MkdirAll(clonePath, 0755); err != nil {
		t.Fatalf("mkdir clone: %v", err)
	}
	pushFailedRun(t, clonePath, "git", "init", "-b", "main")
	pushFailedRun(t, clonePath, "git", "config", "user.email", "test@test.com")
	pushFailedRun(t, clonePath, "git", "config", "user.name", "Test User")
	pushFailedRun(t, clonePath, "git", "remote", "add", "origin", remote)

	if err := os.WriteFile(filepath.Join(clonePath, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	pushFailedRun(t, clonePath, "git", "add", ".")
	pushFailedRun(t, clonePath, "git", "commit", "-m", "initial")
	pushFailedRun(t, clonePath, "git", "push", "-u", "origin", "main")

	return townRoot, clonePath
}

func pushFailedCommit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	pushFailedRun(t, dir, "git", "add", ".")
	pushFailedRun(t, dir, "git", "commit", "-m", msg)
}

// The shape that produced the false escalations: the push failed at the time,
// then the branch reached origin anyway.
func TestPushFailedWorkPreservedWhenBranchReachedOrigin(t *testing.T) {
	townRoot, clonePath := pushFailedFixture(t, "gastown", "coma")

	pushFailedRun(t, clonePath, "git", "checkout", "-b", "polecat/coma/hq-1")
	pushFailedCommit(t, clonePath, "feat.txt", "feature\n", "feat: work")
	pushFailedRun(t, clonePath, "git", "push", "origin", "polecat/coma/hq-1")

	preserved, measured := pushFailedWorkPreserved(townRoot, "gastown", "coma", "polecat/coma/hq-1")
	if !measured || !preserved {
		t.Errorf("pushFailedWorkPreserved() = (preserved %v, measured %v), want the branch on origin to read as preserved", preserved, measured)
	}
}

// The squashed landing: the branch ref is gone and per-commit patch ids no
// longer match, but the content is in main and nothing is at risk.
func TestPushFailedWorkPreservedWhenContentLandedBySquash(t *testing.T) {
	townRoot, clonePath := pushFailedFixture(t, "gastown", "warboy")

	pushFailedRun(t, clonePath, "git", "checkout", "-b", "polecat/warboy/hq-2")
	pushFailedCommit(t, clonePath, "a.txt", "a\n", "WIP: checkpoint")
	pushFailedCommit(t, clonePath, "c.txt", "c\n", "feat: real work")

	pushFailedRun(t, clonePath, "git", "checkout", "main")
	pushFailedRun(t, clonePath, "git", "merge", "--squash", "polecat/warboy/hq-2")
	pushFailedRun(t, clonePath, "git", "commit", "-m", "squashed landing")
	pushFailedRun(t, clonePath, "git", "push", "origin", "main")
	pushFailedRun(t, clonePath, "git", "checkout", "polecat/warboy/hq-2")

	preserved, measured := pushFailedWorkPreserved(townRoot, "gastown", "warboy", "polecat/warboy/hq-2")
	if !measured || !preserved {
		t.Errorf("pushFailedWorkPreserved() = (preserved %v, measured %v), want squash-landed content to read as preserved", preserved, measured)
	}
}

// The alarm this whole gate exists for. It must survive the fix.
func TestPushFailedWorkNotPreservedWhenBranchNeverReachedOrigin(t *testing.T) {
	townRoot, clonePath := pushFailedFixture(t, "gastown", "nux")

	pushFailedRun(t, clonePath, "git", "checkout", "-b", "polecat/nux/hq-3")
	pushFailedCommit(t, clonePath, "stranded.txt", "stranded\n", "feat: never pushed")

	preserved, measured := pushFailedWorkPreserved(townRoot, "gastown", "nux", "polecat/nux/hq-3")
	if preserved {
		t.Errorf("pushFailedWorkPreserved() = (preserved %v, measured %v), want stranded work to keep the alarm", preserved, measured)
	}
}

// Preservation is measured from HEAD. If the worktree has moved off the branch
// the escalation names, the measurement answers a different question, so it must
// report itself unmeasured rather than stand the alarm down.
func TestPushFailedWorkUnmeasuredWhenWorktreeIsOnAnotherBranch(t *testing.T) {
	townRoot, clonePath := pushFailedFixture(t, "gastown", "morsov")

	pushFailedRun(t, clonePath, "git", "checkout", "-b", "polecat/morsov/hq-4")
	pushFailedCommit(t, clonePath, "stranded.txt", "stranded\n", "feat: never pushed")
	pushFailedRun(t, clonePath, "git", "checkout", "main")

	preserved, measured := pushFailedWorkPreserved(townRoot, "gastown", "morsov", "polecat/morsov/hq-4")
	if measured || preserved {
		t.Errorf("pushFailedWorkPreserved() = (preserved %v, measured %v), want unmeasured when HEAD is not the named branch", preserved, measured)
	}
}

// No worktree at all is the case the original escalation was written for: it is
// unmeasurable, so the alarm stands.
func TestPushFailedWorkUnmeasuredWhenWorktreeIsMissing(t *testing.T) {
	townRoot := t.TempDir()

	preserved, measured := pushFailedWorkPreserved(townRoot, "gastown", "ghost", "polecat/ghost/hq-5")
	if measured || preserved {
		t.Errorf("pushFailedWorkPreserved() = (preserved %v, measured %v), want unmeasured for a missing worktree", preserved, measured)
	}
}
