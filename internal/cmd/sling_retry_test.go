package cmd

import (
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
