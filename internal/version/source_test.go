package version

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sourceRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := newGitRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "gt"), 0755); err != nil {
		t.Fatal(err)
	}
	commit := gitCommit(t, dir, "cmd/gt/main.go", "package main\nfunc main() {}\n")
	gitRun(t, dir, "branch", "-M", "main")
	return dir, commit
}

func setSourceProvenance(t *testing.T, repo, ref string) {
	t.Helper()
	oldRepo, oldRef := SourceRepo, SourceRef
	t.Cleanup(func() { SourceRepo, SourceRef = oldRepo, oldRef })
	SourceRepo, SourceRef = repo, ref
}

// Reproduce the town: the mayor has a separate upstream clone; workers use a
// shared bare fork. The fork binary is not part of the mayor's build lineage.
func TestSourceRepoTownTopology(t *testing.T) {
	upstream, base := sourceRepo(t)
	town := t.TempDir()
	mayor := filepath.Join(town, "gastown", "mayor", "rig")
	gitRun(t, town, "clone", "-q", upstream, mayor)
	fork := filepath.Join(town, "fork")
	gitRun(t, town, "clone", "-q", upstream, fork)
	binary := gitCommit(t, fork, "fork.go", "fork implementation")
	tip := gitCommit(t, fork, "later.go", "later implementation")
	bare := filepath.Join(town, "gastown", ".repo.git")
	gitRun(t, town, "clone", "-q", "--bare", fork, bare)
	gitRun(t, bare, "remote", "set-url", "origin", "https://example.test/owner/gastown")
	gitRun(t, bare, "remote", "add", "upstream", "https://example.test/upstream/gastown")
	gitRun(t, bare, "config", "branch.main.remote", "origin")
	gitRun(t, bare, "config", "branch.main.merge", "refs/heads/main")
	gitRun(t, bare, "update-ref", "refs/remotes/upstream/main", base)
	worktree := filepath.Join(town, "worker")
	gitRun(t, bare, "worktree", "add", "-q", "-b", "feature/work", worktree, "main")
	gitCommit(t, worktree, "feature.go", "unmerged")
	setBinaryCommit(t, binary)
	setSourceProvenance(t, bare, "refs/heads/main")
	t.Setenv("GT_ROOT", town)
	t.Setenv("HOME", t.TempDir())

	root, err := GetRepoRoot()
	if err != nil || root != bare {
		t.Fatalf("GetRepoRoot = %q, %v; want shared fork %q", root, err, bare)
	}
	info := CheckStaleBinary(root)
	if info.Error != nil || info.Skipped || !info.IsStale || !info.IsForward || info.CommitsBehind != 1 || info.RepoCommit != tip {
		t.Fatalf("wrong fork comparison: %+v", info)
	}
	if info.OnMainBranch {
		t.Fatal("bare repo must not authorize a worktree rebuild")
	}
	if info.RemoteURL != "https://example.test/owner/gastown" || info.ResolvedRef != "refs/heads/main" || info.RepoCommonDir != bare {
		t.Fatalf("missing repository/ref evidence: %+v", info)
	}

	selected, err := selectSourceRepo(bare, worktree, binary, []string{mayor})
	if err != nil || selected != worktree {
		t.Fatalf("recorded worktree lineage: %q, %v", selected, err)
	}
	info = CheckStaleBinary(selected)
	if info.RepoCommit != tip || info.OnMainBranch {
		t.Fatalf("feature work must not be build target: %+v", info)
	}
	gitRun(t, bare, "worktree", "remove", "--force", worktree)
	selected, err = selectSourceRepo(bare, worktree, binary, []string{mayor})
	if err != nil || selected != bare {
		t.Fatalf("removed build worktree: %q, %v", selected, err)
	}

	// Legacy/trimpath build without surviving provenance still resolves the
	// unique clone containing the fork commit; candidate order is irrelevant.
	selected, err = selectSourceRepo("", "", binary, []string{mayor, bare})
	if err != nil || selected != bare {
		t.Fatalf("fallback: %q, %v", selected, err)
	}
	if got := gitRun(t, mayor, "rev-parse", "main"); got != base {
		t.Fatal("mayor clone changed")
	}
}

func TestSourceRepoAmbiguousOrUnverifiable(t *testing.T) {
	repo, binary := sourceRepo(t)
	clone := filepath.Join(t.TempDir(), "clone")
	gitRun(t, repo, "clone", "-q", repo, clone)
	for _, candidates := range [][]string{{repo, clone}, {clone, repo}} {
		if _, err := selectSourceRepo("", "", binary, candidates); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("independent clones must be ambiguous: %v", err)
		}
	}
	// Identical URLs are not evidence that two clones are the same source.
	gitRun(t, repo, "remote", "add", "origin", "https://example.test/same")
	gitRun(t, clone, "remote", "set-url", "origin", "https://example.test/same")
	if _, err := selectSourceRepo("", "", binary, []string{repo, clone}); err == nil {
		t.Fatal("equal URLs must not resolve ambiguity")
	}
	if got, err := selectSourceRepo(repo, "", binary, []string{clone}); err != nil || got != repo {
		t.Fatalf("recorded source: %q, %v", got, err)
	}
	for _, commit := range []string{"", "ffffffffffffffffffffffffffffffffffffffff"} {
		if _, err := selectSourceRepo("", "", commit, []string{repo}); err == nil {
			t.Fatal("unverifiable commit accepted")
		}
	}
	other := newGitRepo(t)
	unrelated := gitCommit(t, other, "go.mod", "module example.test/other")
	if _, err := selectSourceRepo("", "", unrelated, []string{other}); err == nil {
		t.Fatal("go.mod alone is not gt source")
	}
	if _, err := selectSourceRepo(other, "", binary, []string{repo}); err == nil {
		t.Fatal("invalid recorded source silently fell back")
	}
	if got, err := selectSourceRepo(filepath.Join(t.TempDir(), "removed"), "", binary, []string{repo}); err != nil || got != repo {
		t.Fatalf("missing source fallback: %q, %v", got, err)
	}
}

func TestSourceRepoDeduplicatesCommonDirectory(t *testing.T) {
	repo, binary := sourceRepo(t)
	wt := filepath.Join(t.TempDir(), "worktree")
	gitRun(t, repo, "worktree", "add", "-q", "--detach", wt)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := selectSourceRepo("", "", binary, []string{repo, wt, alias, filepath.Join(repo, ".git")}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceRefSurvivesWorktreeBranchSwitch(t *testing.T) {
	repo, binary := sourceRepo(t)
	tip := gitCommit(t, repo, "main.go", "main update")
	gitRun(t, repo, "checkout", "-q", "-b", "carry/other")
	gitCommit(t, repo, "other.go", "other build branch")
	setBinaryCommit(t, binary)
	setSourceProvenance(t, repo, "refs/heads/main")
	info := CheckStaleBinary(repo)
	if info.RepoCommit != tip || info.OnMainBranch {
		t.Fatalf("must use recorded ref and refuse rebuild on another branch: %+v", info)
	}
	gitRun(t, repo, "branch", "-D", "main")
	info = CheckStaleBinary(repo)
	if !info.Skipped || info.IsStale {
		t.Fatalf("missing recorded ref must skip: %+v", info)
	}
}

func TestTrackedBuildRemoteBeatsUnrelatedUpstream(t *testing.T) {
	repo, base := sourceRepo(t)
	forkTip := gitCommit(t, repo, "fork.go", "fork")
	gitRun(t, repo, "remote", "add", "my-fork", "https://example.test/fork")
	gitRun(t, repo, "update-ref", "refs/remotes/my-fork/main", forkTip)
	gitRun(t, repo, "config", "branch.main.remote", "my-fork")
	gitRun(t, repo, "config", "branch.main.merge", "refs/heads/main")
	gitRun(t, repo, "checkout", "-q", "-b", "other", base)
	upstreamTip := gitCommit(t, repo, "upstream.go", "upstream")
	gitRun(t, repo, "update-ref", "refs/remotes/upstream/main", upstreamTip)
	ref, ok := resolveBuildBranchRef(repo, base)
	if !ok || ref.commit != forkTip {
		t.Fatalf("tracking configuration must select fork: %+v, %v", ref, ok)
	}
	if got := refRemoteURL(repo, ref.ref); got != "https://example.test/fork" {
		t.Fatalf("remote evidence = %q", got)
	}
}
