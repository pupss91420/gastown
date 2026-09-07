package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// NotificationStartupFixture replaces only tmux transport, maintaining a live
// session marker and creation count. All paths and drain failures are temporary.
func NotificationStartupFixture(t *testing.T, sessionName string) (string, func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX tmux fixture")
	}
	root := t.TempDir()
	for _, dir := range []string{"mayor", "bin", ".runtime"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Force an actual poller startup failure without launching any child.
	if err := os.WriteFile(filepath.Join(root, ".runtime", "nudge_poller"), []byte("blocks PID directory"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_TEST_SESSION_NAME", sessionName)
	t.Setenv("GT_TEST_SESSION_STATE", root)
	script := `#!/bin/sh
while [ "$1" = "-u" ] || [ "$1" = "-L" ]; do
 if [ "$1" = "-L" ]; then shift; fi
 shift
done
command="$1"
shift
case "$command" in
 has-session) if ! test -f "$GT_TEST_SESSION_STATE/live"; then printf "can't find session: fixture\n" >&2; exit 1; fi ;;
 new-session) touch "$GT_TEST_SESSION_STATE/live"; printf 'spawn\n' >> "$GT_TEST_SESSION_STATE/spawns" ;;
 kill-session) rm -f "$GT_TEST_SESSION_STATE/live" ;;
 list-sessions) printf '%s|1|1|0|1|1\n' "$GT_TEST_SESSION_NAME" ;;
 show-environment)
  for key do :; done
  case "$key" in
   GT_AGENT) printf 'GT_AGENT=codex\n' ;;
   GT_PROCESS_NAMES) printf 'GT_PROCESS_NAMES=codex\n' ;;
   GT_PANE_ID) printf 'GT_PANE_ID=%%0\n' ;;
  esac ;;
 display-message)
  case "$*" in
   *pane_current_command*) printf 'codex\n' ;;
   *pane_pid*) printf '999999999\n' ;;
   *pane_id*) printf '%%0\n' ;;
  esac ;;
 capture-pane)
  if test -f "$GT_TEST_SESSION_STATE/busy"; then
   printf 'Working (esc to interrupt)\n'
  else
   printf '› \n'
  fi ;;
 send-keys)
  case "$*" in *Enter*|*C-m*) touch "$GT_TEST_SESSION_STATE/busy" ;; esac ;;
esac
`
	if err := os.WriteFile(filepath.Join(root, "bin", "tmux"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Chdir(root)
	return root, func() {
		t.Helper()
		if _, err := os.Stat(filepath.Join(root, "live")); err != nil {
			t.Fatal("successful agent is no longer live", err)
		}
		data, err := os.ReadFile(filepath.Join(root, "spawns"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(data), "spawn") != 1 {
			t.Fatalf("duplicate spawn: %q", data)
		}
	}
}
