package cmd

import (
	"path/filepath"
	"testing"
)

// Bare remotes and worktrees are entirely temporary; no network or town state.
func TestRecoveryRequiresCurrentRemotePreservation(t *testing.T) {
	for _, scenario := range []string{"pushed", "stale behind", "deleted branch", "offline", "dirty", "stash", "unpushed", "landed", "rewound main", "squash landed", "split remote", "unavailable push remote", "unknown remote tip"} {
		t.Run(scenario, func(t *testing.T) {
			repo := setupGitStateRemoteRepo(t)
			runGitCmd(t, repo, "commit", "--allow-empty", "-m", "unique metadata")
			writeTestFile(t, filepath.Join(repo, "work"), "unique work\n")
			runGitCmd(t, repo, "add", "work")
			runGitCmd(t, repo, "commit", "-m", "work")
			if scenario != "unpushed" {
				runGitCmd(t, repo, "push", "origin", "HEAD")
			}
			remote := filepath.Join(filepath.Dir(repo), "remote.git")
			switch scenario {
			case "split remote", "unavailable push remote":
				if scenario == "split remote" {
					runGitCmd(t, repo, "remote", "set-url", "--push", "origin", remote)
					runGitCmd(t, repo, "remote", "set-url", "origin", filepath.Join(repo, "absent.git"))
				} else {
					runGitCmd(t, repo, "remote", "set-url", "--push", "origin", filepath.Join(repo, "absent.git"))
				}
			case "squash landed":
				runGitCmd(t, repo, "switch", "main")
				runGitCmd(t, repo, "merge", "--squash", "integration/test")
				runGitCmd(t, repo, "commit", "-m", "squashed work")
				runGitCmd(t, repo, "push", "origin", "main")
				runGitCmd(t, repo, "switch", "integration/test")
				runGitCmd(t, remote, "update-ref", "-d", "refs/heads/integration/test")
			case "unknown remote tip":
				other := filepath.Join(filepath.Dir(repo), "other")
				runGitCmd(t, repo, "clone", "--branch", "integration/test", remote, other)
				runGitCmd(t, other, "config", "user.email", "test@example.com")
				runGitCmd(t, other, "config", "user.name", "Test")
				writeTestFile(t, filepath.Join(other, "other-work"), "remote only")
				runGitCmd(t, other, "add", "other-work")
				runGitCmd(t, other, "commit", "-m", "remote only")
				runGitCmd(t, other, "push", "origin", "HEAD")
			case "stale behind":
				runGitCmd(t, repo, "update-ref", "refs/remotes/origin/integration/test", "HEAD~2")
			case "deleted branch":
				runGitCmd(t, remote, "update-ref", "-d", "refs/heads/integration/test")
			case "offline":
				runGitCmd(t, repo, "remote", "set-url", "origin", filepath.Join(repo, "absent.git"))
			case "dirty":
				writeTestFile(t, filepath.Join(repo, "dirty"), "uncommitted")
			case "stash":
				writeTestFile(t, filepath.Join(repo, "work"), "stashed")
				runGitCmd(t, repo, "stash")
			case "landed", "rewound main":
				runGitCmd(t, repo, "push", "origin", "HEAD:main")
				runGitCmd(t, remote, "update-ref", "-d", "refs/heads/integration/test")
				if scenario == "rewound main" {
					runGitCmd(t, remote, "update-ref", "refs/heads/main", "refs/heads/main~2")
				}
			}
			want := scenario == "pushed" || scenario == "stale behind" || scenario == "landed" || scenario == "squash landed" || scenario == "split remote"
			if got := activeMRGitSafeForWorktree(repo); got != want {
				t.Fatalf("safe = %t, want %t", got, want)
			}
		})
	}
}
