package version

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/steveyegge/gastown/internal/util"
)

// SourceRepo and SourceRef are stamped by make build. The common directory
// survives removal of the worktree that built the binary. URLs are evidence,
// not repository identity: a clone may intentionally have several remotes.
var SourceRepo string
var SourceRef string

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func isGitRepo(dir string) bool {
	_, err := gitOutput(dir, "rev-parse", "--absolute-git-dir")
	return err == nil
}

// GetRepoRoot locates the binary's build repository. A recorded common
// directory or compiler source path takes precedence over town layout. Without
// usable provenance, only a unique clone with a verifiable build lineage is
// accepted; directory order and remote URL spelling cannot establish identity.
func GetRepoRoot() (string, error) {
	var compiledSource string
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		compiledSource = filepath.Dir(filepath.Dir(filepath.Dir(file)))
	}
	var candidates []string
	addTown := func(root string) {
		candidates = append(candidates, root, filepath.Join(root, ".repo.git"), filepath.Join(root, "mayor", "rig"))
	}
	if root := os.Getenv("GT_ROOT"); root != "" {
		addTown(filepath.Join(root, "gastown"))
	}
	if home := os.Getenv("HOME"); home != "" {
		for _, root := range []string{filepath.Join(home, "gt", "gastown"), filepath.Join(home, "gastown"), filepath.Join(home, "src", "gastown")} {
			addTown(root)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		if root, err := gitOutput(cwd, "rev-parse", "--show-toplevel"); err == nil {
			candidates = append(candidates, root)
		}
	}
	return selectSourceRepo(SourceRepo, compiledSource, resolveCommitHash(), candidates)
}

func sourceCommonDir(dir, commit string) (string, bool) {
	if dir == "" || commit == "" {
		return "", false
	}
	resolved, err := resolveGitCommit(dir, commit)
	if err != nil {
		return "", false
	}
	// Validate the committed source, which also works in bare repositories and
	// avoids trusting an untracked cmd/gt/main.go in an unrelated checkout.
	if _, err := gitOutput(dir, "cat-file", "-e", resolved+":cmd/gt/main.go"); err != nil {
		return "", false
	}
	common, err := gitOutput(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", false
	}
	common, err = filepath.EvalSymlinks(common)
	return common, err == nil
}

func selectSourceRepo(recorded, compiled, commit string, candidates []string) (string, error) {
	if commit == "" {
		return "", fmt.Errorf("cannot determine binary commit; source lineage is unverifiable")
	}
	// A surviving recorded repository must validate. Do not silently switch to
	// an unrelated clone when this path exists but no longer has our source.
	if recorded != "" {
		if _, err := os.Stat(recorded); err == nil {
			common, ok := sourceCommonDir(recorded, commit)
			if !ok {
				return "", fmt.Errorf("recorded build repository %q cannot verify binary source %s", recorded, ShortCommit(commit))
			}
			if compiledCommon, ok := sourceCommonDir(compiled, commit); ok && compiledCommon == common {
				return compiled, nil
			}
			return recorded, nil
		}
	}
	if _, ok := sourceCommonDir(compiled, commit); ok {
		return compiled, nil
	}
	matches := make(map[string]string)
	for _, candidate := range candidates {
		common, ok := sourceCommonDir(candidate, commit)
		if !ok {
			continue
		}
		if _, exists := matches[common]; !exists {
			matches[common] = candidate
		}
	}
	if len(matches) == 1 {
		for _, candidate := range matches {
			return candidate, nil
		}
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous gt source lineage: %d independent repositories contain binary %s; rebuild with source provenance", len(matches), ShortCommit(commit))
	}
	return "", fmt.Errorf("cannot locate a verified gt source repository for binary %s", ShortCommit(commit))
}

// refRemoteURL reports only the remote bound to the selected ref. In
// particular, origin is not assumed to be the owner of an untracked local ref.
func refRemoteURL(repoDir, ref string) string {
	remote := ""
	if strings.HasPrefix(ref, "refs/remotes/") {
		rest := strings.TrimPrefix(ref, "refs/remotes/")
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			remote = rest[:i]
		}
	} else if strings.HasPrefix(ref, "refs/heads/") {
		remote, _ = gitOutput(repoDir, "config", "--get", "branch."+strings.TrimPrefix(ref, "refs/heads/")+".remote")
	}
	if remote == "" || remote == "." {
		return ""
	}
	url, _ := gitOutput(repoDir, "remote", "get-url", remote)
	return url
}
