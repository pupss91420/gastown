package channelevents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/lock"
)

// MigrateLegacyToRig moves only fully attributable flat events into their rig's
// directory. An atomic rename transfers custody once; a channel lock serializes
// concurrent daemon/consumer migration. Unknown or malformed events stay intact.
// There is no global "done" marker: old producers may still emit during rollout.
func MigrateLegacyToRig(townRoot, channel, rig string) error {
	scoped, err := Directory(townRoot, channel, rig)
	if err != nil {
		return err
	}
	if !RigScoped(channel) {
		return nil
	}
	flat := filepath.Dir(scoped)
	if _, err := os.Stat(flat); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	unlock, err := lock.FlockAcquire(filepath.Join(flat, ".migration.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := os.ReadDir(flat)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".event") {
			continue
		}
		source := filepath.Join(flat, entry.Name())
		data, err := os.ReadFile(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var event struct {
			Type    string `json:"type"`
			Channel string `json:"channel"`
			Payload struct {
				Rig string `json:"rig"`
			} `json:"payload"`
		}
		if json.Unmarshal(data, &event) != nil || event.Type == "" || event.Channel != channel || event.Payload.Rig != rig {
			continue
		}
		if err := os.MkdirAll(scoped, 0755); err != nil {
			return err
		}
		// New producers use numeric filenames, so reserve a legacy prefix. Refuse
		// an unexpected collision instead of overwriting either piece of evidence.
		dest := filepath.Join(scoped, "legacy-"+entry.Name())
		if _, err := os.Lstat(dest); err == nil {
			return fmt.Errorf("legacy event destination already exists: %s", dest)
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(source, dest); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("migrating legacy event: %w", err)
		}
	}
	return nil
}
