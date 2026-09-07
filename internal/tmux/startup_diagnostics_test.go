package tmux

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreserveStartupFailureSurvivesPaneLoss(t *testing.T) {
	root := t.TempDir()
	cause := errors.New("startup observation failed: pane disappeared")
	err := PreserveStartupFailure(root, "gt-probe", &startupFailure{err: cause, screen: "Quick safety check\n❯ No, exit\nYes, I trust this folder"})
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "startup diagnostics:") {
		t.Fatalf("classification lost: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(root, "logs", "startup", "failure-*.json"))
	if len(files) != 1 {
		t.Fatalf("missing diagnostic file: %v", files)
	}
	info, _ := os.Stat(files[0])
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("diagnostic is not private: %v", info.Mode())
	}
	data, readErr := os.ReadFile(files[0])
	if readErr != nil {
		t.Fatal(readErr)
	}
	var report map[string]string
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report["session"] != "gt-probe" || !strings.Contains(report["last_screen"], "No, exit") {
		t.Fatalf("evidence lost: %s", data)
	}
}
