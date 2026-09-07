package channelevents

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLegacyMigrationRoutesOnce(t *testing.T) {
	root := t.TempDir()
	for _, channel := range []string{"refinery", "witness"} {
		t.Run(channel, func(t *testing.T) {
			flat := filepath.Join(root, "events", channel)
			if err := os.MkdirAll(flat, 0755); err != nil {
				t.Fatal(err)
			}
			fixtures := map[string]string{
				"alpha.event":         `{"type":"WAKE","channel":"` + channel + `","payload":{"rig":"alpha"}}`,
				"beta.event":          `{"type":"WAKE","channel":"` + channel + `","payload":{"rig":"beta"}}`,
				"ambiguous.event":     `{"type":"WAKE","channel":"` + channel + `","payload":{}}`,
				"malformed.event":     `{`,
				"wrong-channel.event": `{"type":"WAKE","channel":"mayor","payload":{"rig":"alpha"}}`,
			}
			for name, data := range fixtures {
				if err := os.WriteFile(filepath.Join(flat, name), []byte(data), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := MigrateLegacyToRig(root, channel, "alpha"); err != nil {
				t.Fatal(err)
			}
			// Alpha migration cannot move/delete Beta's source file.
			if _, err := os.Stat(filepath.Join(flat, "beta.event")); err != nil {
				t.Fatal("other rig source changed", err)
			}
			// Repeated/concurrent daemon and consumer calls retain one copy per rig.
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				for _, rig := range []string{"alpha", "beta"} {
					wg.Add(1)
					go func(rig string) {
						defer wg.Done()
						if err := MigrateLegacyToRig(root, channel, rig); err != nil {
							t.Error(err)
						}
					}(rig)
				}
			}
			wg.Wait()
			for _, rig := range []string{"alpha", "beta"} {
				dir := filepath.Join(flat, rig)
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 {
					t.Fatalf("%s got %d copies", rig, len(entries))
				}
				data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(data, []byte(fixtures[rig+".event"])) {
					t.Fatal("migration changed event bytes")
				}
				if _, err := os.Stat(filepath.Join(flat, rig+".event")); !os.IsNotExist(err) {
					t.Fatal("source still exists after transfer")
				}
			}
			for _, name := range []string{"ambiguous.event", "malformed.event", "wrong-channel.event"} {
				data, err := os.ReadFile(filepath.Join(flat, name))
				if err != nil || string(data) != fixtures[name] {
					t.Fatalf("unattributable %s changed: %v", name, err)
				}
			}
		})
	}
}

func TestLegacyMigrationCollisionPreservesBoth(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "events", "refinery", "alpha")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(filepath.Dir(dir), "one.event")
	if err := os.WriteFile(source, []byte(`{"channel":"refinery","type":"WAKE","payload":{"rig":"alpha"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "legacy-one.event")
	if err := os.WriteFile(dest, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacyToRig(root, "refinery", "alpha"); err == nil {
		t.Fatal("collision accepted")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("source lost", err)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "existing" {
		t.Fatal("destination overwritten")
	}
}
