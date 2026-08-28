package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The landing gate decides whether a polecat's committed work is durable. It is
// the input to cleanup_status, to `gt patrol scan`'s zombie classification, and
// to the PUSH_FAILED "possible work loss" escalation, so it has to be right in
// both directions: landed work must read as landed, and genuinely unpushed work
// must still alarm. A gate that only ever says "landed" is not a fix, it is a
// regression that removes the one signal an operator has (hq-wsw).
//
// The fixtures below are the three landing shapes measured in gastown, plus the
// mirror-upstream shape reported independently in AlphaPrime.

func landingRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return string(out)
}

func landingCommit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	landingRun(t, dir, "git", "add", ".")
	landingRun(t, dir, "git", "commit", "-m", msg)
}

// A rebased landing gives the work a new SHA on main, so ancestry says no. The
// patch is unchanged, so the gate must still read it as landed.
func TestLandingGateAcceptsRebasedLanding(t *testing.T) {
	local, _, main := initTestRepoWithRemote(t)
	g := NewGit(local)

	landingRun(t, local, "git", "checkout", "-b", "polecat/x/hq-rebased")
	landingCommit(t, local, "feat.txt", "feature\n", "feat: add feature")
	landingRun(t, local, "git", "push", "origin", "polecat/x/hq-rebased")

	landingRun(t, local, "git", "checkout", main)
	landingCommit(t, local, "other.txt", "other\n", "unrelated work")
	landingRun(t, local, "git", "cherry-pick", "polecat/x/hq-rebased")
	landingRun(t, local, "git", "push", "origin", main)
	// The branch ref is deleted on merge, so the branch's own remote ref cannot
	// answer the question either.
	landingRun(t, local, "git", "push", "origin", "--delete", "polecat/x/hq-rebased")
	landingRun(t, local, "git", "fetch", "--prune", "origin")
	landingRun(t, local, "git", "checkout", "polecat/x/hq-rebased")

	pushed, unpushed, err := g.BranchPushedToRemote("polecat/x/hq-rebased", "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if !pushed || unpushed != 0 {
		t.Errorf("rebased landing = (pushed %v, unpushed %d), want landed with 0 unpushed", pushed, unpushed)
	}
}

// A squashed landing collapses several commits into one, so every per-commit
// patch id differs and `git cherry` alone cannot see the landing. Only a content
// comparison settles it.
func TestLandingGateAcceptsSquashedLanding(t *testing.T) {
	local, _, main := initTestRepoWithRemote(t)
	g := NewGit(local)

	landingRun(t, local, "git", "checkout", "-b", "polecat/x/hq-squashed")
	landingCommit(t, local, "a.txt", "a\n", "WIP: checkpoint")
	landingCommit(t, local, "b.txt", "b\n", "WIP: checkpoint")
	landingCommit(t, local, "c.txt", "c\n", "feat: real work")
	landingRun(t, local, "git", "push", "origin", "polecat/x/hq-squashed")

	landingRun(t, local, "git", "checkout", main)
	landingCommit(t, local, "other.txt", "other\n", "unrelated work")
	landingRun(t, local, "git", "merge", "--squash", "polecat/x/hq-squashed")
	landingRun(t, local, "git", "commit", "-m", "squashed landing")
	landingRun(t, local, "git", "push", "origin", main)
	landingRun(t, local, "git", "push", "origin", "--delete", "polecat/x/hq-squashed")
	landingRun(t, local, "git", "fetch", "--prune", "origin")
	landingRun(t, local, "git", "checkout", "polecat/x/hq-squashed")

	// Guard the fixture: this must be a shape `git cherry` cannot resolve, or the
	// test stops covering the squash case it exists for.
	cherry, err := g.Cherry("origin/"+main, "HEAD")
	if err != nil {
		t.Fatalf("Cherry: %v", err)
	}
	if CountCherryUnmergedCommits(cherry) == 0 {
		t.Fatalf("fixture no longer exercises a squash: cherry already reports it landed:\n%s", cherry)
	}

	pushed, unpushed, err := g.BranchPushedToRemote("polecat/x/hq-squashed", "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if !pushed || unpushed != 0 {
		t.Errorf("squashed landing = (pushed %v, unpushed %d), want landed with 0 unpushed", pushed, unpushed)
	}
}

// The other direction. Work that never reached anywhere durable must still
// alarm, or PUSH_FAILED stops meaning anything.
func TestLandingGateStillAlarmsOnGenuinelyUnpushedWork(t *testing.T) {
	local, _, _ := initTestRepoWithRemote(t)
	g := NewGit(local)

	landingRun(t, local, "git", "checkout", "-b", "polecat/x/hq-unpushed")
	landingCommit(t, local, "never.txt", "never pushed\n", "feat: never pushed")

	pushed, unpushed, err := g.BranchPushedToRemote("polecat/x/hq-unpushed", "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if pushed || unpushed == 0 {
		t.Errorf("genuinely unpushed work = (pushed %v, unpushed %d), want not-landed with unpushed > 0", pushed, unpushed)
	}
}

// Work landed on the branch but partly superseded on main must still alarm for
// the part that is not there: containment, not mere relatedness, is the test.
func TestLandingGateStillAlarmsOnPartiallyLandedWork(t *testing.T) {
	local, _, main := initTestRepoWithRemote(t)
	g := NewGit(local)

	landingRun(t, local, "git", "checkout", "-b", "polecat/x/hq-partial")
	landingCommit(t, local, "landed.txt", "landed\n", "feat: this one lands")
	landingRun(t, local, "git", "checkout", main)
	landingRun(t, local, "git", "cherry-pick", "polecat/x/hq-partial")
	landingRun(t, local, "git", "push", "origin", main)

	landingRun(t, local, "git", "checkout", "polecat/x/hq-partial")
	landingCommit(t, local, "stranded.txt", "stranded\n", "feat: this one does not")

	pushed, unpushed, err := g.BranchPushedToRemote("polecat/x/hq-partial", "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if pushed || unpushed == 0 {
		t.Errorf("partially landed work = (pushed %v, unpushed %d), want not-landed with unpushed > 0", pushed, unpushed)
	}
}

// The worktree's upstream can be a mirror that landings never reach — a
// push-disabled remote, or a stale tracking ref. It must not be the sole gate:
// the landing branch is what decides whether the work is durable.
func TestLandingGateDoesNotTrustPushDisabledMirrorUpstream(t *testing.T) {
	local, _, main := initTestRepoWithRemote(t)
	g := NewGit(local)

	mirror := filepath.Join(t.TempDir(), "mirror.git")
	landingRun(t, local, "git", "init", "--bare", mirror)
	landingRun(t, local, "git", "remote", "add", "localsrc", mirror)
	landingRun(t, local, "git", "push", "localsrc", main)
	landingRun(t, local, "git", "remote", "set-url", "--push", "localsrc", "no-push")

	landingRun(t, local, "git", "checkout", "-b", "polecat/x/hq-mirror")
	landingCommit(t, local, "feat.txt", "feature\n", "feat: add feature")

	// The work lands on origin/main, the real landing branch.
	landingRun(t, local, "git", "checkout", main)
	landingRun(t, local, "git", "cherry-pick", "polecat/x/hq-mirror")
	landingRun(t, local, "git", "push", "origin", main)
	landingRun(t, local, "git", "checkout", "polecat/x/hq-mirror")
	// ...but the branch tracks the mirror, which never receives landings.
	landingRun(t, local, "git", "branch", "--set-upstream-to=localsrc/"+main, "polecat/x/hq-mirror")
	landingRun(t, local, "git", "fetch", "origin")

	pushed, unpushed, err := g.BranchPushedToRemote("polecat/x/hq-mirror", "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if !pushed || unpushed != 0 {
		t.Errorf("landed work with mirror upstream = (pushed %v, unpushed %d), want landed with 0 unpushed", pushed, unpushed)
	}
}

// A mirror upstream must not flip the other direction either: when the work
// really is stranded, the landing-branch fallback must not excuse it.
func TestLandingGateStillAlarmsWithMirrorUpstreamAndStrandedWork(t *testing.T) {
	local, _, main := initTestRepoWithRemote(t)
	g := NewGit(local)

	mirror := filepath.Join(t.TempDir(), "mirror.git")
	landingRun(t, local, "git", "init", "--bare", mirror)
	landingRun(t, local, "git", "remote", "add", "localsrc", mirror)
	landingRun(t, local, "git", "push", "localsrc", main)
	landingRun(t, local, "git", "remote", "set-url", "--push", "localsrc", "no-push")

	landingRun(t, local, "git", "checkout", "-b", "polecat/x/hq-stranded")
	landingCommit(t, local, "stranded.txt", "stranded\n", "feat: goes nowhere")
	landingRun(t, local, "git", "branch", "--set-upstream-to=localsrc/"+main, "polecat/x/hq-stranded")

	pushed, unpushed, err := g.BranchPushedToRemote("polecat/x/hq-stranded", "origin")
	if err != nil {
		t.Fatalf("BranchPushedToRemote: %v", err)
	}
	if pushed || unpushed == 0 {
		t.Errorf("stranded work with mirror upstream = (pushed %v, unpushed %d), want not-landed with unpushed > 0", pushed, unpushed)
	}
}

// An explicitly named target that cannot be resolved is a failed measurement,
// not a clean result. The landing-branch fallback must not paper over it.
func TestLandingGateReportsUnresolvableTargetsAsError(t *testing.T) {
	local, _, _ := initTestRepoWithRemote(t)
	g := NewGit(local)

	landingRun(t, local, "git", "checkout", "-b", "polecat/x/hq-target")
	landingCommit(t, local, "feat.txt", "feature\n", "feat: add feature")

	if _, err := g.BranchTargetStatus("polecat/x/hq-target", "origin", []string{"refs/heads/no-such-target"}); err == nil {
		t.Error("BranchTargetStatus with an unresolvable target should report a measurement failure, not a verdict")
	}
}
