package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/channelevents"
	"github.com/steveyegge/gastown/internal/workspace"
)

// resolveEventScope keeps CLI producers and consumers on the same channel.
// An explicit recipient wins over the caller's rig; never infer "mayor" as a rig.
func resolveEventScope(channel, requestedRig string) (string, string, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", "", err
	}
	rigName := requestedRig
	if channelevents.RigScoped(channel) && rigName == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", "", err
		}
		rigName = detectRigFromPath(townRoot, cwd)
		if rigName == "" {
			rigName = os.Getenv("GT_RIG")
		}
		if rigName == "" {
			return "", "", fmt.Errorf("channel %q requires --rig outside a rig", channel)
		}
	}
	if _, err := channelevents.Directory(townRoot, channel, rigName); err != nil {
		return "", "", err
	}
	if channelevents.RigScoped(channel) {
		if info, err := os.Stat(filepath.Join(townRoot, rigName)); err != nil || !info.IsDir() {
			return "", "", fmt.Errorf("rig %q does not exist in %s", rigName, townRoot)
		}
	}
	return townRoot, rigName, nil
}
