// Package channelevents provides file-based event emission for named channels.
//
// Channel events are JSON files written to ~/gt/events/<channel>/*.event
// and consumed by await-event subscribers (e.g., the refinery watching for
// MERGE_READY events). This is distinct from the activity feed events in
// the events package (~/gt/.events.jsonl).
package channelevents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/steveyegge/gastown/internal/workspace"
)

// ValidChannelName restricts channel names to safe characters (no path traversal).
var ValidChannelName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// emitSeq is an atomic counter to ensure unique event filenames even when
// time.Now().UnixNano() has low resolution.
var emitSeq atomic.Uint64

// Emit creates an event file in the channel directory, resolving the town
// root from the current working directory.
func Emit(channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		home, _ := os.UserHomeDir()
		townRoot = filepath.Join(home, "gt")
	}
	return EmitToTown(townRoot, channel, eventType, payloadPairs)
}

// RigScoped reports whether a channel has one consumer per rig.
func RigScoped(channel string) bool {
	return channel == "refinery" || channel == "witness"
}

// Directory is shared by producers and consumers. Rig-scoped channels never
// fall back to the legacy town-wide directory, including during cleanup.
func Directory(townRoot, channel, rig string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}
	dir := filepath.Join(townRoot, "events", channel)
	if RigScoped(channel) {
		if !ValidChannelName.MatchString(rig) {
			return "", fmt.Errorf("channel %q requires a valid rig name (use --rig)", channel)
		}
		return filepath.Join(dir, rig), nil
	}
	return dir, nil
}

// EmitToTown creates an event with an explicit town root. Rig-scoped channels
// require a rig payload; callers that know the recipient should use EmitToRig.
func EmitToTown(townRoot, channel, eventType string, payloadPairs []string) (string, error) {
	rig := ""
	for _, pair := range payloadPairs {
		if key, value, ok := strings.Cut(pair, "="); ok && key == "rig" {
			rig = value
		}
	}
	return EmitToRig(townRoot, rig, channel, eventType, payloadPairs)
}

// EmitToRig emits to the recipient rig, recording its identity in the payload.
// Town-wide channels (such as mayor) retain their single shared directory.
func EmitToRig(townRoot, rig, channel, eventType string, payloadPairs []string) (string, error) {
	eventDir, err := Directory(townRoot, channel, rig)
	if err != nil {
		return "", err
	}
	if RigScoped(channel) {
		payloadPairs = append(append([]string(nil), payloadPairs...), "rig="+rig)
	}
	if err := os.MkdirAll(eventDir, 0755); err != nil {
		return "", fmt.Errorf("creating event directory: %w", err)
	}
	return emitToDir(eventDir, channel, eventType, payloadPairs)
}

// emitToDir writes an event file to the given directory.
func emitToDir(eventDir, channel, eventType string, payloadPairs []string) (string, error) {
	if !ValidChannelName.MatchString(channel) {
		return "", fmt.Errorf("invalid channel name %q: must match [a-zA-Z0-9_-]", channel)
	}

	payload := make(map[string]string)
	for _, pair := range payloadPairs {
		key, val, found := strings.Cut(pair, "=")
		if found {
			payload[key] = val
		}
	}

	now := time.Now()
	event := map[string]interface{}{
		"type":      eventType,
		"channel":   channel,
		"timestamp": now.Format(time.RFC3339),
		"payload":   payload,
	}

	data, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling event: %w", err)
	}

	seq := emitSeq.Add(1)
	eventFile := filepath.Join(eventDir, fmt.Sprintf("%d-%d-%d.event", now.UnixNano(), seq, os.Getpid()))
	if err := os.WriteFile(eventFile, data, 0644); err != nil {
		return "", fmt.Errorf("writing event file: %w", err)
	}

	return eventFile, nil
}
