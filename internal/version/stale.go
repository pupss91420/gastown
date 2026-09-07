// Package version provides version information and staleness checking for gt.
package version

import (
	"fmt"
	"os/exec"
	"runtime/debug"
	"strings"

	"github.com/steveyegge/gastown/internal/util"
)

// These variables are set at build time via ldflags in cmd package.
// We provide fallback methods to read from build info.
var (
	// Commit can be set from cmd package or read from build info
	Commit = ""
)

// StaleBinaryInfo contains information about binary staleness.
type StaleBinaryInfo struct {
	RepoRoot      string // Selected source repository (may be bare)
	RepoCommonDir string // Git common directory identifying the clone
	RemoteURL     string // Remote associated with the comparison ref, if configured
	ResolvedRef   string // Fully qualified comparison ref
	IsStale       bool   // True if binary commit is behind the build-branch ref
	IsForward     bool   // True if the compare commit is a descendant of binary commit (safe to rebuild)
	OnMainBranch  bool   // True if the resolved source worktree is on a build branch
	BinaryCommit  string // Commit hash the binary was built from
	RepoCommit    string // Commit of the ref the binary was compared against (CompareRef)
	CompareRef    string // The ref staleness was computed against (e.g. "main", "origin/main")
	CommitsBehind int    // Number of commits binary is behind (0 if unknown)
	Skipped       bool   // True if staleness could not be determined safely
	SkipReason    string // Human-readable reason the check was skipped
	Error         error  // Any error encountered during check
}

type buildBranchRef struct {
	ref     string
	display string
	commit  string
}

// resolveCommitHash gets the commit hash from build info or the Commit variable.
func resolveCommitHash() string {
	if Commit != "" {
		return Commit
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value
			}
		}
	}

	return ""
}

// Describe returns a one-line, human-readable staleness summary for a stale
// binary, using subject as the leading noun so callers can vary it
// ("Binary" for gt doctor, "gt binary" for the startup warning):
//
//	"Binary is 3 commits behind main (built from abc123…, main at def456…)"
//	"gt binary is stale (built from abc123…, origin/main at def456…)"
//
// It is only meaningful when i.IsStale; callers gate on that. A zero
// CommitsBehind (count unknown) falls back to the "is stale" wording.
func (i *StaleBinaryInfo) Describe(subject string) string {
	if i.CommitsBehind > 0 {
		return fmt.Sprintf("%s is %d commits behind %s (built from %s, %s at %s)",
			subject, i.CommitsBehind, i.CompareRef,
			ShortCommit(i.BinaryCommit), i.CompareRef, ShortCommit(i.RepoCommit))
	}
	return fmt.Sprintf("%s is stale (built from %s, %s at %s)",
		subject, ShortCommit(i.BinaryCommit), i.CompareRef, ShortCommit(i.RepoCommit))
}

// ShortCommit returns first 12 characters of a hash.
func ShortCommit(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// commitsMatch compares two commit hashes, handling different lengths.
// Returns true if one is a prefix of the other (minimum 7 chars to avoid false positives).
func commitsMatch(a, b string) bool {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	// Need at least 7 chars for a reasonable comparison
	if minLen < 7 {
		return false
	}
	return strings.HasPrefix(a, b[:minLen]) || strings.HasPrefix(b, a[:minLen])
}

// CheckStaleBinary compares the binary's embedded commit with a build-branch
// ref. It returns staleness info including whether the binary needs rebuilding.
// This check is designed to be fast and non-blocking - errors are captured but
// don't interrupt normal operation.
func CheckStaleBinary(repoDir string) *StaleBinaryInfo {
	info := &StaleBinaryInfo{RepoRoot: repoDir}

	// Get binary commit
	info.BinaryCommit = resolveCommitHash()
	if info.BinaryCommit == "" {
		info.Error = fmt.Errorf("cannot determine binary commit (dev build?)")
		return info
	}
	if !isGitRepo(repoDir) {
		info.Error = fmt.Errorf("source repo %q is not a git repository", repoDir)
		return info
	}
	info.RepoCommonDir, _ = gitOutput(repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	binaryCommit, err := resolveGitCommit(repoDir, info.BinaryCommit)
	if err != nil {
		info.Skipped = true
		info.SkipReason = "binary commit not found in source repo; cannot compare staleness"
		return info
	}

	// Check which branch the resolved source worktree is on.
	// Accept main/master (upstream) and carry/* (fork operational branches).
	var branch string
	branchCmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	branchCmd.Dir = repoDir
	util.SetDetachedProcessGroup(branchCmd)
	if branchOutput, err := branchCmd.Output(); err == nil {
		branch = strings.TrimSpace(string(branchOutput))
	}
	inside, _ := gitOutput(repoDir, "rev-parse", "--is-inside-work-tree")
	info.OnMainBranch = isBuildBranch(branch) && inside == "true"

	// An embedded build ref remains authoritative even if the build worktree
	// has since switched branches or has been removed.
	var compareCommit string
	if strings.HasPrefix(SourceRef, "refs/heads/") && isBuildBranch(strings.TrimPrefix(SourceRef, "refs/heads/")) {
		info.ResolvedRef = SourceRef
		info.CompareRef = strings.TrimPrefix(SourceRef, "refs/heads/")
		compareCommit, err = resolveGitCommit(repoDir, SourceRef)
		info.OnMainBranch = info.OnMainBranch && branch == info.CompareRef
		if err != nil {
			info.Skipped = true
			info.SkipReason = "recorded build ref is unavailable in source repository"
			return info
		}
	} else if isBuildBranch(branch) {
		info.CompareRef = branch
		info.ResolvedRef = "refs/heads/" + branch
		compareCommit, err = resolveGitCommit(repoDir, info.ResolvedRef)
		if err != nil {
			info.Error = fmt.Errorf("cannot resolve build branch: %w", err)
			return info
		}
	} else {
		ref, ok := resolveBuildBranchRef(repoDir, binaryCommit)
		if !ok {
			info.Skipped = true
			info.SkipReason = "no unambiguous build-branch lineage found to compare against"
			return info
		}
		info.CompareRef = ref.display
		info.ResolvedRef = ref.ref
		compareCommit = ref.commit
	}
	info.RemoteURL = refRemoteURL(repoDir, info.ResolvedRef)
	info.RepoCommit = compareCommit

	// Compare commits using prefix matching (handles short vs full hash)
	// Use the shorter of the two commit lengths for comparison
	if !commitsMatch(info.BinaryCommit, info.RepoCommit) {
		// Check if all commits between binary and the build ref only touch
		// .beads/ files (e.g., bd backup commits). These don't affect the
		// binary and should not trigger a stale warning. (GH#2596)
		if onlyBeadsChanges(repoDir, binaryCommit, compareCommit) {
			// Build ref advanced but only via beads-only commits — not stale
			return info
		}

		info.IsStale = true

		// Check if this is a forward-only update (binary commit is ancestor of
		// the build ref). This prevents rebuilding to an older or diverged
		// commit, which caused a crash loop when a worktree's HEAD was behind
		// the binary's commit.
		info.IsForward = isAncestor(repoDir, binaryCommit, compareCommit)

		// Try to count commits between binary and the build ref
		countCmd := exec.Command("git", "rev-list", "--count", binaryCommit+".."+compareCommit)
		countCmd.Dir = repoDir
		util.SetDetachedProcessGroup(countCmd)
		if countOutput, err := countCmd.Output(); err == nil {
			if count, parseErr := fmt.Sscanf(strings.TrimSpace(string(countOutput)), "%d", &info.CommitsBehind); parseErr != nil || count != 1 {
				info.CommitsBehind = 0
			}
		}
	}

	return info
}

// resolveBuildBranchRef finds a build-branch ref to compare the binary against
// when the resolved source worktree is parked on a non-build branch (the normal
// state for $GT_ROOT/gastown/mayor/rig). Without this, staleness would be
// computed against unmerged feature work (GH#4034).
//
// Candidate refs are fully qualified to avoid branch/tag shadowing. Among refs
// that contain the binary commit, choose the freshest descendant; only use the
// candidate order below to break equivalent ties. Diverged tips are ambiguous.
func resolveBuildBranchRef(repoDir, binaryCommit string) (buildBranchRef, bool) {
	var usable []buildBranchRef
	for _, candidate := range buildBranchCandidates(repoDir) {
		commit, err := resolveGitCommit(repoDir, candidate.ref)
		if err != nil || !isAncestor(repoDir, binaryCommit, commit) {
			continue
		}
		candidate.commit = commit
		usable = append(usable, candidate)
	}
	if len(usable) == 0 {
		return buildBranchRef{}, false
	}

	frontier := make([]buildBranchRef, 0, len(usable))
	for i, candidate := range usable {
		older := false
		for j, other := range usable {
			if i == j || candidate.commit == other.commit {
				continue
			}
			if isAncestor(repoDir, candidate.commit, other.commit) {
				older = true
				break
			}
		}
		if !older {
			frontier = append(frontier, candidate)
		}
	}
	for _, candidate := range frontier[1:] {
		if candidate.commit != frontier[0].commit {
			return buildBranchRef{}, false
		}
	}
	return frontier[0], true
}

func buildBranchCandidates(repoDir string) []buildBranchRef {
	// A local build branch's tracking configuration identifies its remote,
	// including custom remote names. Do not mix unrelated upstream/fork refs
	// into this comparison just because both descend from an old binary.
	var tracked []buildBranchRef
	out, _ := gitOutput(repoDir, "for-each-ref", "--format=%(refname) %(upstream)", "refs/heads/")
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !isBuildBranch(strings.TrimPrefix(fields[0], "refs/heads/")) {
			continue
		}
		tracked = append(tracked,
			buildBranchRef{ref: fields[0], display: strings.TrimPrefix(fields[0], "refs/heads/")},
			buildBranchRef{ref: fields[1], display: strings.TrimPrefix(fields[1], "refs/remotes/")})
	}
	if len(tracked) > 0 {
		return tracked
	}

	candidates := make([]buildBranchRef, 0, 10)
	for _, pattern := range []string{
		"refs/heads/carry/",
		"refs/remotes/upstream/carry/",
		"refs/remotes/origin/carry/",
	} {
		if ref, ok := singleBranchRef(repoDir, pattern); ok {
			candidates = append(candidates, ref)
		}
	}
	candidates = append(candidates,
		buildBranchRef{ref: "refs/remotes/upstream/main", display: "upstream/main"},
		buildBranchRef{ref: "refs/remotes/upstream/master", display: "upstream/master"},
		buildBranchRef{ref: "refs/remotes/origin/main", display: "origin/main"},
		buildBranchRef{ref: "refs/remotes/origin/master", display: "origin/master"},
		buildBranchRef{ref: "refs/heads/main", display: "main"},
		buildBranchRef{ref: "refs/heads/master", display: "master"},
	)
	return candidates
}

func resolveGitCommit(repoDir, rev string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// isAncestor reports whether ancestor is an ancestor of ref (a commit is its
// own ancestor) in repoDir.
func isAncestor(repoDir, ancestor, ref string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, ref)
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	return cmd.Run() == nil
}

// singleBranchRef returns the sole matching branch/ref, if exactly one exists.
// Multiple matches are ambiguous and yield false.
func singleBranchRef(repoDir, pattern string) (buildBranchRef, bool) {
	cmd := exec.Command("git", "for-each-ref", "--format=%(refname)", pattern)
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return buildBranchRef{}, false
	}
	refs := strings.Fields(strings.TrimSpace(string(out)))
	if len(refs) != 1 {
		return buildBranchRef{}, false
	}
	display := strings.TrimPrefix(refs[0], "refs/heads/")
	display = strings.TrimPrefix(display, "refs/remotes/")
	return buildBranchRef{ref: refs[0], display: display}, true
}

// onlyBeadsChanges checks whether all commits between binaryCommit and
// compareRef exclusively modify files under .beads/. Returns true if the diff
// contains no changes outside .beads/, meaning the binary is functionally
// up-to-date. Used to suppress false-positive stale warnings from bd backup
// commits. (GH#2596)
func onlyBeadsChanges(repoDir, binaryCommit, compareRef string) bool {
	// Get files changed between binary commit and the build ref, excluding
	// .beads/. If this produces no output, all changes are within .beads/
	cmd := exec.Command("git", "diff", "--name-only", binaryCommit+".."+compareRef, "--", ":(top)**", ":(top,exclude).beads/**")
	cmd.Dir = repoDir
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	if err != nil {
		// Can't determine — be conservative, assume stale
		return false
	}
	return strings.TrimSpace(string(output)) == ""
}

// isBuildBranch returns true if the given branch is safe for automated rebuilds.
// Accepted branches:
//   - main, master: upstream default branches
//   - carry/*: fork operational branches (e.g., carry/operational)
//
// This prevents automated rebuilds from random feature, fix, or polecat branches
// which could cause downgrades or crash loops.
func isBuildBranch(branch string) bool {
	switch branch {
	case "main", "master":
		return true
	}
	return strings.HasPrefix(branch, "carry/")
}

// SetCommit allows the cmd package to pass in the build-time commit.
func SetCommit(commit string) {
	Commit = commit
}
