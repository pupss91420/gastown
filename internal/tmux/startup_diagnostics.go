package tmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// startupFailure retains the last successful observation when a pane vanishes.
// The screen is deliberately omitted from Error() and saved privately instead.
type startupFailure struct {
	err    error
	screen string
}

func (e *startupFailure) Error() string { return e.err.Error() }
func (e *startupFailure) Unwrap() error { return e.err }

// PreserveStartupFailure saves evidence outside the disposable polecat worktree
// before rollback destroys the session. It never changes the failure classification.
func PreserveStartupFailure(townRoot, session string, err error) error {
	if err == nil || townRoot == "" {
		return err
	}
	var failure *startupFailure
	if !errors.As(err, &failure) {
		return err
	}
	data, marshalErr := json.MarshalIndent(struct {
		Session    string `json:"session"`
		Error      string `json:"error"`
		LastScreen string `json:"last_screen"`
	}{session, err.Error(), failure.screen}, "", "  ")
	if marshalErr != nil {
		return err
	}
	dir := filepath.Join(townRoot, "logs", "startup")
	if saveErr := os.MkdirAll(dir, 0700); saveErr != nil {
		return fmt.Errorf("%w (saving startup diagnostics: %v)", err, saveErr)
	}
	file, saveErr := os.CreateTemp(dir, "failure-*.json")
	if saveErr != nil {
		return fmt.Errorf("%w (saving startup diagnostics: %v)", err, saveErr)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return fmt.Errorf("%w (saving startup diagnostics: %v)", err, errors.Join(writeErr, closeErr))
	}
	return fmt.Errorf("%w (startup diagnostics: %s)", err, file.Name())
}
