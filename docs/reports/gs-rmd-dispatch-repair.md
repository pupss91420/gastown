# Dispatch startup and retry repair (gs-rmd)

The installed runtime reported `gt version 1.2.1` with source module SHA
`95e33a0d1bde`. The implementation worktree started at
`df5b8d87cfbfe75bb2bd3484b9571c25fa536a05`, with origin
`https://github.com/pupss91420/gastown`.

The existing reproduction used `gt sling ap-zek.1/.2 AlphaPrime --review-only
--hook-raw-bead --no-merge`. The captured Claude 2.1.263 screen in
`mayor/reports/alphaprime-audit-20260907/startup-diagnostic.txt` shows
`No, exit` selected and `Yes, I trust this folder` second. Reading
`internal/tmux/tmux.go` at the installed module SHA confirms that trust acceptance
sent bare Enter under an explicit assumption that option one accepts trust.
That assumption is false for the captured menu. The new live tmux regression
uses a raw terminal program which exits on bare Enter and survives only when the
correct selection is delivered; it verifies separate navigation and confirmation.
The first real revised Claude probe (`gs-vpr`, `gs-dementus`) still timed out
with its menu unchanged. The next revision sends navigation alone and requires
a fresh capture showing the affirmative selection before sending Enter; the
fixture rejects combined navigation/Enter input. Coalesced TUI input is the
working explanation for the real failure, pending another runtime probe.

A separate masking defect used `CapturePane(..., 80)`, which includes scrollback
via `-S -80`. Prompt detection recognized trailing prompt glyphs but missed a
populated Codex composer. Historical trust text above that composer could thus
be classified as an active modal after the worker had started.

The repair selects trust by the rendered affirmative label and selection marker,
rejects unknown layouts without guessing, scans the current viewport, recognizes
populated composers without treating menu selections as prompts, and waits for
late dialogs within the runtime startup budget. The regression delays the trust
screen for nine seconds, beyond the prior eight-second dialog window. Capture
errors and acceptance errors propagate as startup failures. Before rollback,
private JSON diagnostics under `logs/startup/` retain the last successful screen;
the error includes the evidence path without dumping pane contents inline.

Retry constraints are saved in `dispatch_constraints` before allocation. Runtime,
account, review-only, no-merge, raw-hook, and ownership settings survive attachment
rollback and are reapplied when a retry omits flags. Explicit runtime/account
choices can replace the corresponding prior values; safety booleans remain set.
Legacy attachment safety fields are also honored. Invalid constraint JSON stops
dispatch. Clearing a safety constraint intentionally requires updating the bead,
not merely omitting a flag. `--owned` still denotes caller-managed convoy lifecycle;
it is not a scheduling exclusion.

Direct and queued sling reject blocked beads unless explicitly forced. Both the
reactive convoy feeder and daemon stranded-convoy feeder check the respawn circuit
before starting a sling subprocess. A shared handler takes the same per-bead
sling lock, reads the current open/unassigned state, creates a fingerprinted
high-severity escalation, verifies a persisted mayor mailbox receipt in that
escalation thread, then blocks the bead and verifies that status persisted.
An escalation process can exit zero with `partial_failure`, and a later invocation
can return `duplicate_suppressed` without delivering the missing mail. Neither
counts as a receipt. The handler reads structured thread-label records through
`bd query --all --limit 0 --json`, verified against installed bd 1.2.2 (which
has no `message thread` command), retries missing mail in the same thread, and verifies persistence before blocking. Failed or
unverified notification remains retryable, rather than becoming silently blocked.
The blocked bead's notes name the convoy, bead, reason and escalation. Completed
work, existing pauses and live assignments are left alone. Status and appended
notes are the only mutations, retaining retry constraints and respawn counters.
The stranded-convoy readiness predicate also explicitly excludes blocked/deferred
statuses instead of considering unassigned paused work orphaned. Existing
per-bead sling locks still serialize dispatch. Rollback re-reads operator state, retains blocked/deferred status, refuses
to unhook another assignee or reopen closed work, verifies the resulting bead state,
and checks session/worktree removal.

RCA environment capture: daemon PID 8674; no concurrent sling seen during the
repair snapshot. Dolt PID 8772, port 3307, query latency 0s and 5/1000 connections.
Stale PID and orphan test server investigation were not needed for this non-Dolt
failure. No Dolt internals, agent account selection, AlphaPrime code, or paused
AlphaPrime audit sessions were changed.

Targeted dialog, retry, rollback, blocked-state and feeder tests pass, including
real tmux keystroke delivery. Repository-wide `go vet ./...` passes. The first full
suite encountered three unrelated failures because inherited `GT_DOLT_PORT=3307`
overrode ports in test fixtures; all three pass with that environment variable
removed from the test process. The corrected full suite passes with `env -u GT_DOLT_PORT go test ./...`.
The later diagnostic-preservation addition also passes targeted tests and vet.
Review found the convoy package's old TestMain exited successfully without running
any tests when Docker was absent. That harness is fixed: pure mock tests run;
only isolated-Dolt store tests skip. Verbose RUN/PASS evidence in
`build/notification-receipt-tests.log` covers both feeders, delivery failure,
undelivered duplicate retry, missing receipt rejection and active-assignee safety.
Stateful command tests also exercise rollback followed by flagless queue and
single-sling retry, preserving runtime/account and review constraints.

The observed-selection candidate passed fresh Claude `gs-hr9` and reused Claude
`gs-m3t` (same directory inode 229205), with exit 0, live panes and exact-hook
report markers. Its Codex probe `gs-fbi` failed: a `model: loading` composer
preceded the trust menu. Treating that placeholder as ready allowed subsequent
startup delivery to race the modal. The loading-guard revision waits for a
non-loading composer stable for 500ms, and runtime readiness also rejects loading
headers and modal selections. The raw-TUI regression reproduces a loading
composer before the delayed trust menu.

Coordinator evidence lives under
`mayor/reports/dispatch-probes-20260907/` in the town workspace. The immutable
loading-guard artifact SHA256 is
`35e25d3a2503733e680a4f7d738651e2abfa738544f203b3aac26f3a42be2ed5`.
Its Codex `gs-fmm`, default Claude `gs-mwr`, and reused Claude `gs-546` all passed
exit/pane/hook/report checks; reuse retained inode 313770. The first Codex pass
showed no trust modal at an already trusted pathname. The decisive `gs-okn`
contrast at `capable` then captured loading composer → actual Codex trust menu →
worker, exact hook, `GS_RMD_LOADING_TRUST_OK` report read-back, live pane and
sling exit 0. The coordinator accepted startup on both observed trust-menu paths
at 2026-09-07 16:14 UTC. Recovery JSON and clean git state were captured before
supported probe cleanup.

The coordinator accepted terminal intervention and completed rollout at
2026-09-07 16:34 UTC. Production source commit `3d78408f` was preserved outside
the worker worktree, with binary SHA256
`24e5a48c93f8d795fa5bbdc0769c3c73929ab7d5deae258de70719e98fa61e06`.
Seven independently executed targeted tests and live read-only receipt-helper
positive/negative controls passed against installed bd 1.2.2. Commit `27661dfb`
adds an exact query-argument test assertion without changing production code.

The coordinator backed up and atomically replaced the installed binary, then
restarted only the daemon (PID 8674 → 10044). The running daemon executable hash
matches the preserved release. All ten existing agent pane PIDs and Dolt PID
8772 were unchanged. Evidence is preserved under `review-3d78408f/` and
`rollout-3d78408f/` in the coordinator report directory above. The coordinator
closed blocker `gs-l65` on this deployed and tested repair. Real AlphaPrime audit
beads remain blocked, their convoys deferred, and respawn counters untouched.
Code integration proceeds through the refinery merge queue; installed rollout
is not a claim that the branch has already merged to main.

Git branch pushes succeeded. The separate Dolt remote-history mismatch prevented
`bd dolt push`; recovery remains tracked as `hq-wayo`. No force push, bootstrap,
or shared database repair was attempted.
