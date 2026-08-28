package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupNamedPolecatTargetTest creates a town root and chdirs into it so
// detectTownRootFromCwd resolves during target classification.
func setupNamedPolecatTargetTest(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor", "rig"), 0755); err != nil {
		t.Fatalf("mkdir mayor/rig: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(filepath.Join(townRoot, "mayor", "rig")); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return townRoot
}

// TestNamedPolecatTargetClassification covers hq-s1t acceptance #2 (both target
// forms resolve identically) and #4 (a bare rig target is not a named target).
func TestNamedPolecatTargetClassification(t *testing.T) {
	townRoot := setupNamedPolecatTargetTest(t)
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", "crew", "max"), 0755); err != nil {
		t.Fatalf("mkdir crew: %v", err)
	}

	tests := []struct {
		target      string
		wantRig     string
		wantPolecat string
		wantOK      bool
	}{
		{target: "gastown/polecats/guzzle", wantRig: "gastown", wantPolecat: "guzzle", wantOK: true},
		{target: "gastown/guzzle", wantRig: "gastown", wantPolecat: "guzzle", wantOK: true},
		{target: "gastown/polecats/Guzzle", wantRig: "gastown", wantPolecat: "guzzle", wantOK: true},
		{target: "gastown/Guzzle", wantRig: "gastown", wantPolecat: "guzzle", wantOK: true},
		// Rig-level target: "any polecat", not a named one.
		{target: "gastown", wantOK: false},
		// Known roles and crew members are not polecats.
		{target: "gastown/witness", wantOK: false},
		{target: "gastown/refinery", wantOK: false},
		{target: "gastown/crew/max", wantOK: false},
		{target: "gastown/max", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			rigName, polecatName, ok := namedPolecatTarget(tc.target, townRoot)
			if ok != tc.wantOK {
				t.Fatalf("namedPolecatTarget(%q) ok = %v, want %v", tc.target, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if rigName != tc.wantRig || polecatName != tc.wantPolecat {
				t.Fatalf("namedPolecatTarget(%q) = (%q, %q), want (%q, %q)",
					tc.target, rigName, polecatName, tc.wantRig, tc.wantPolecat)
			}
		})
	}
}

// TestResolveTargetNamedPolecatPinsSpawnToThatPolecat is the core hq-s1t
// regression: a named polecat with no live pane must never be swapped for a
// different polecat. Both target spellings must pin the same name, with or
// without --create.
func TestResolveTargetNamedPolecatPinsSpawnToThatPolecat(t *testing.T) {
	for _, target := range []string{"gastown/polecats/guzzle", "gastown/guzzle"} {
		for _, create := range []bool{true, false} {
			name := target
			if create {
				name += " --create"
			}
			t.Run(name, func(t *testing.T) {
				townRoot := setupNamedPolecatTargetTest(t)

				prevResolve := resolveTargetAgentFn
				prevSpawn := spawnPolecatForSling
				t.Cleanup(func() {
					resolveTargetAgentFn = prevResolve
					spawnPolecatForSling = prevSpawn
				})
				resolveTargetAgentFn = func(string) (string, string, string, error) {
					return "", "", "", errors.New("getting pane for gt-guzzle: exit status 1")
				}

				var gotOpts SlingSpawnOptions
				spawnCalled := false
				spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
					spawnCalled = true
					gotOpts = opts
					if rigName != "gastown" {
						t.Fatalf("rigName = %q, want gastown", rigName)
					}
					// Production pins the name; mirror that here so the agent
					// assertion below is meaningful.
					return &SpawnedPolecatInfo{
						RigName:     rigName,
						PolecatName: opts.PolecatName,
						ClonePath:   filepath.Join(townRoot, "fake-polecat"),
					}, nil
				}

				got, err := resolveTarget(target, ResolveTargetOptions{
					Create:   create,
					NoBoot:   true,
					TownRoot: townRoot,
				})
				if err != nil {
					t.Fatalf("resolveTarget: %v", err)
				}
				if !spawnCalled {
					t.Fatal("expected spawnPolecatForSling to be called")
				}
				if gotOpts.PolecatName != "guzzle" {
					t.Fatalf("PolecatName = %q, want guzzle (sling must not reassign to another polecat)", gotOpts.PolecatName)
				}
				if got.Agent != "gastown/polecats/guzzle" {
					t.Fatalf("Agent = %q, want gastown/polecats/guzzle", got.Agent)
				}
			})
		}
	}
}

// TestResolveTargetNamedPolecatFailureNamesPolecat covers hq-s1t acceptance #1's
// second branch: when the named polecat can't take the work, the failure names
// it instead of quietly picking someone else.
func TestResolveTargetNamedPolecatFailureNamesPolecat(t *testing.T) {
	townRoot := setupNamedPolecatTargetTest(t)

	prevResolve := resolveTargetAgentFn
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() {
		resolveTargetAgentFn = prevResolve
		spawnPolecatForSling = prevSpawn
	})
	resolveTargetAgentFn = func(string) (string, string, string, error) {
		return "", "", "", errors.New("getting pane for gt-guzzle: exit status 1")
	}
	spawnPolecatForSling = func(string, SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return nil, errors.New("polecat gastown/guzzle does not exist")
	}

	_, err := resolveTarget("gastown/polecats/guzzle", ResolveTargetOptions{
		NoBoot:   true,
		TownRoot: townRoot,
	})
	if err == nil {
		t.Fatal("expected error when the named polecat cannot take the work")
	}
	if !strings.Contains(err.Error(), "guzzle") {
		t.Fatalf("error must name the polecat, got: %v", err)
	}
}

// TestResolveTargetNamedPolecatDryRunDoesNotSpawn guards the trap this bead's
// family keeps producing: a --dry-run that performs the real side effect. The
// rig-level path already returned early; the named-polecat path did not, and the
// shorthand form now reaches it without --create.
func TestResolveTargetNamedPolecatDryRunDoesNotSpawn(t *testing.T) {
	townRoot := setupNamedPolecatTargetTest(t)

	prevResolve := resolveTargetAgentFn
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() {
		resolveTargetAgentFn = prevResolve
		spawnPolecatForSling = prevSpawn
	})
	resolveTargetAgentFn = func(string) (string, string, string, error) {
		return "", "", "", errors.New("getting pane for gt-guzzle: exit status 1")
	}
	spawnPolecatForSling = func(string, SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		t.Fatal("--dry-run must not spawn or restart a polecat")
		return nil, nil
	}

	got, err := resolveTarget("gastown/polecats/guzzle", ResolveTargetOptions{
		DryRun:   true,
		NoBoot:   true,
		TownRoot: townRoot,
	})
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if got.Agent != "gastown/polecats/guzzle" {
		t.Fatalf("Agent = %q, want gastown/polecats/guzzle", got.Agent)
	}
	if got.NewPolecatInfo != nil {
		t.Fatal("dry run must not report a spawned polecat")
	}
}
