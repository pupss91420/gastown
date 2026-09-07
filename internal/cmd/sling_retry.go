package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
)

// dispatchConstraints survives startup rollback. A retry may choose a new
// runtime explicitly, but omission must never erase the previous contract.
type dispatchConstraints struct {
	Agent       string `json:"agent,omitempty"`
	Account     string `json:"account,omitempty"`
	ReviewOnly  bool   `json:"review_only,omitempty"`
	NoMerge     bool   `json:"no_merge,omitempty"`
	HookRawBead bool   `json:"hook_raw_bead,omitempty"`
	Owned       bool   `json:"owned,omitempty"`
}

func mergeDispatchConstraints(info *beadInfo, requested dispatchConstraints) (dispatchConstraints, error) {
	requested.NoMerge = requested.NoMerge || requested.ReviewOnly
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: info.Description})
	if fields == nil {
		return requested, nil
	}
	var prior dispatchConstraints
	if fields.DispatchConstraints != "" {
		if err := json.Unmarshal([]byte(fields.DispatchConstraints), &prior); err != nil {
			return requested, fmt.Errorf("invalid persisted dispatch constraints: %w", err)
		}
	}
	if requested.Agent == "" {
		requested.Agent = prior.Agent
	}
	if requested.Account == "" {
		requested.Account = prior.Account
	}
	requested.ReviewOnly = requested.ReviewOnly || prior.ReviewOnly || fields.ReviewOnly
	requested.NoMerge = requested.NoMerge || prior.NoMerge || fields.NoMerge || requested.ReviewOnly
	requested.HookRawBead = requested.HookRawBead || prior.HookRawBead
	requested.Owned = requested.Owned || prior.Owned || fields.ConvoyOwned
	return requested, nil
}

func persistDispatchConstraints(townRoot, beadID string, info *beadInfo, constraints dispatchConstraints) error {
	payload, err := json.Marshal(constraints)
	if err != nil {
		return err
	}
	if string(payload) == "{}" {
		return nil
	}
	issue := &beads.Issue{Description: info.Description}
	fields := beads.ParseAttachmentFields(issue)
	if fields == nil {
		fields = &beads.AttachmentFields{}
	}
	if fields.DispatchConstraints == string(payload) {
		return nil
	}
	fields.DispatchConstraints = string(payload)
	description := beads.SetAttachmentFields(issue, fields)
	if err := BdCmd("update", beadID, "--description="+description).Dir(resolveBeadDirFromTownRoot(townRoot, beadID)).StripBeadsDir().WithAutoCommit().Run(); err != nil {
		return fmt.Errorf("persisting dispatch constraints: %w", err)
	}
	info.Description = description
	return nil
}
