package cmd

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestDispatchConstraintsSurviveRollback(t *testing.T) {
	description := `dispatch_constraints: {"agent":"codex","account":"team","review_only":true,"no_merge":true,"hook_raw_bead":true,"owned":true}
review_only: true
no_merge: true

Probe`
	// Rollback clears active attachment flags; the retry contract must survive.
	issue := &beads.Issue{Description: description}
	fields := beads.ParseAttachmentFields(issue)
	fields.ReviewOnly, fields.NoMerge = false, false
	info := &beadInfo{Description: beads.SetAttachmentFields(issue, fields)}
	got, err := mergeDispatchConstraints(info, dispatchConstraints{})
	want := dispatchConstraints{Agent: "codex", Account: "team", ReviewOnly: true, NoMerge: true, HookRawBead: true, Owned: true}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
	got, err = mergeDispatchConstraints(info, dispatchConstraints{Agent: "claude-gastown"})
	if err != nil || got.Agent != "claude-gastown" || !got.ReviewOnly {
		t.Fatalf("explicit runtime override lost safety: %+v %v", got, err)
	}
}

func TestDispatchConstraintsInvalidFailsClosed(t *testing.T) {
	if _, err := mergeDispatchConstraints(&beadInfo{Description: "dispatch_constraints: broken"}, dispatchConstraints{}); err == nil {
		t.Fatal("malformed constraints ignored")
	}
}

func TestDispatchConstraintsLegacyReview(t *testing.T) {
	got, err := mergeDispatchConstraints(&beadInfo{Description: "review_only: true\nconvoy_owned: true"}, dispatchConstraints{})
	if err != nil || !got.ReviewOnly || !got.NoMerge || !got.Owned {
		t.Fatalf("legacy constraints lost: %+v %v", got, err)
	}
}

func TestRollbackUnhookRetainsOperatorState(t *testing.T) {
	for _, tt := range []struct {
		status, assignee, want string
		allowed                bool
	}{
		{"blocked", "gastown/polecats/probe", "blocked", true},
		{"hooked", "gastown/polecats/probe", "open", true},
		{"closed", "", "", false},
		{"hooked", "gastown/polecats/other", "", false},
	} {
		got, allowed := rollbackUnhookStatus(&beadInfo{Status: tt.status, Assignee: tt.assignee}, "gastown/polecats/probe")
		if got != tt.want || allowed != tt.allowed {
			t.Fatalf("%+v: got %q %t", tt, got, allowed)
		}
	}
}

func TestConvoyPausedBeadNeverReady(t *testing.T) {
	for _, status := range []string{"blocked", "deferred"} {
		for _, assignee := range []string{"", "gastown/polecats/missing"} {
			if isReadyIssue(trackedIssueInfo{ID: "gt-paused", Status: status, Assignee: assignee}, nil) {
				t.Fatalf("paused bead considered ready: %s %q", status, assignee)
			}
		}
	}
}

func TestDispatchRetryReplaysConstraintsAfterFailure(t *testing.T) {
	town, rigPath, description := setupMutableBDRawSlingTest(t, "Probe")
	previousSpawn, previousHook := spawnPolecatForSling, hookBeadWithRetryWithTownRootFn
	previousSingleHook := hookBeadWithRetryFn
	t.Cleanup(func() { hookBeadWithRetryFn = previousSingleHook })
	t.Cleanup(func() { spawnPolecatForSling, hookBeadWithRetryWithTownRootFn = previousSpawn, previousHook })
	spawns, hooks := 0, 0
	spawnPolecatForSling = func(rig string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawns++
		if opts.Agent != "codex" || opts.Account != "probe-account" {
			t.Fatalf("retry lost runtime/account: %+v", opts)
		}
		return &SpawnedPolecatInfo{RigName: rig, PolecatName: "probe", ClonePath: filepath.Join(town, "gastown", "polecats", "probe")}, nil
	}
	hookBeadWithRetryWithTownRootFn = func(bead, agent, hookDir, root string) error {
		hooks++
		assertHasRawReviewMetadata(t, readMutableBDDescription(t, description))
		return errors.New("probe startup boundary failure")
	}
	hookBeadWithRetryFn = func(bead, agent, hookDir string) error {
		return hookBeadWithRetryWithTownRootFn(bead, agent, hookDir, town)
	}
	requested := SlingParams{BeadID: "gt-rawrollback", RigName: "gastown", TownRoot: town, BeadsDir: filepath.Join(rigPath, ".beads"), Agent: "codex", Account: "probe-account", ReviewOnly: true, NoMerge: true, HookRawBead: true, Owned: true, NoConvoy: true, NoBoot: true}
	if _, err := executeSling(requested); err == nil {
		t.Fatal("expected injected failure")
	}
	assertNoRawReviewMetadata(t, readMutableBDDescription(t, description))
	// Queue/batch retry without original flags, with a default formula that raw
	// dispatch must suppress, still restores the same contract before hooking.
	retry := SlingParams{BeadID: requested.BeadID, RigName: requested.RigName, TownRoot: town, BeadsDir: requested.BeadsDir, FormulaName: "must-not-cook", NoConvoy: true, NoBoot: true}
	if _, err := executeSling(retry); err == nil {
		t.Fatal("expected injected retry failure")
	}
	if spawns != 2 || hooks != 2 {
		t.Fatalf("retry did not reach guarded hook: spawns=%d hooks=%d", spawns, hooks)
	}
	// Both convoy feeder paths invoke this minimal single-sling command. Exercise
	// its replay path independently of the queue path above.
	oldAgent, oldAccount, oldFormula := slingAgent, slingAccount, slingFormula
	oldReview, oldMerge, oldRaw, oldOwned := slingReviewOnly, slingNoMerge, slingHookRawBead, slingOwned
	oldConvoy, oldBoot, oldDry, oldForce := slingNoConvoy, slingNoBoot, slingDryRun, slingForce
	t.Cleanup(func() {
		slingAgent, slingAccount, slingFormula = oldAgent, oldAccount, oldFormula
		slingReviewOnly, slingNoMerge, slingHookRawBead, slingOwned = oldReview, oldMerge, oldRaw, oldOwned
		slingNoConvoy, slingNoBoot, slingDryRun, slingForce = oldConvoy, oldBoot, oldDry, oldForce
	})
	slingAgent, slingAccount, slingFormula = "", "", ""
	slingReviewOnly, slingNoMerge, slingHookRawBead, slingOwned = false, false, false, false
	slingNoConvoy, slingNoBoot, slingDryRun, slingForce = true, true, false, false
	if err := runSling(nil, []string{requested.BeadID, "gastown"}); err == nil {
		t.Fatal("expected injected single-sling failure")
	}
	if spawns != 3 || hooks != 3 {
		t.Fatalf("single-sling lost replay path: spawns=%d hooks=%d", spawns, hooks)
	}
}
