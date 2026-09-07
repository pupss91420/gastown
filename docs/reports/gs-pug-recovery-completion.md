# Recovery and completion repair: gs-pug / gs-3vp

Baseline: fork main `1c3015d1`, including deployed gs-rmd (`3d78408f`).
Read the actual charter at `internal/templates/roles/polecat.md.tmpl`;
the assignment explicitly authorizes local fork Refinery submission.

Reviewed `c8ccc95b`, `95e33a0d`, and `d203f54d` before adaptation.
`95e33a0d` is the separate dog-done guard fix (gs-4o8), excluded from this scope.

## Implemented

- Adapted c8ccc95b's sticky push-failure reconciliation without reverting the
  newer cleanup-status or MQ classification rules. It requires terminal work,
  an absent/terminal hook, nonpending active MR, successful target lookup, and
  current remote preservation. Other recovery blockers remain effective.
- Strengthened the custody probe: the older helper could fall back to stale
  tracking refs even when the remote was unavailable. Only hashes advertised
  now by the push remote may prove preservation. Dirty work, stashes, deleted
  custody, remote failure, and unknown remote objects fail closed. No remote
  fetch or runtime flag clearing is performed by this probe.
- Adapted d203f54d's missing liveness check after the final readiness wait and
  before working-state writes. Both observed death and unknown liveness fail
  dispatch, with distinct errors. All three dispatch paths report failure and
  qualify their earlier hook acknowledgement as session pending.
- Added rollback retries and verification while retaining gs-rmd's re-read of
  operator intent. Blocked/deferred work remains paused, reassigned/terminal
  work is untouched, and unverifiable release emits STRANDED WORK.

## Semantic overlap and bounds

Current SessionManager already verifies startup dialogs, readiness, and session
health. Those checks are retained; the added check covers the later readiness
wait in StartSession. Existing terminal limiter, retry constraints, trust UI,
and molecule cleanup code remain in place. The older cosmetic
`hooked-no-session` inventory state was not imported: existing lifecycle and
capacity classification remain authoritative. This delivers the missing
spawn/rollback safeguards, not the old patch byte-for-byte.

Validation uses fake liveness/bead callbacks and temporary local Git repositories
and bare remotes. Tests cover stale/deleted refs, offline and split push remotes,
squash landing, unknown remote objects, dirty/stashed/unpushed work, rollback
write loss/errors, operator pauses, reassignment, terminal work, and retained
recovery blockers. No live spawn or runtime acceptance is claimed. Mayor owns
runtime acceptance and cleanup; installed binary and production data unchanged.

Commands: `go build ./cmd/gt`; `go vet ./internal/cmd ./internal/polecat ./internal/git`;
`go test ./internal/cmd -run 'Test(RecoveryRequiresCurrentRemotePreservation|VerifySessionLive|VerifyRollbackUnhook|RollbackUnhookRetainsOperatorState|DispatchConstraints)' -count=1`;
`go test ./internal/polecat -run 'Test(CanIgnoreStalePushFailed|DecideWorkstate|ReconciledPushFailure)' -count=1`.
