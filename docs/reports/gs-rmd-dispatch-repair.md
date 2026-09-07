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
before starting a sling subprocess. Existing per-bead sling locks still serialize
dispatch. Rollback re-reads operator state, retains blocked/deferred status, refuses
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

Real runtime acceptance remains required. Disposable probe beads `gs-vpr`
(default configured Claude route) and `gs-nml` (explicit Codex) require the worker
to read its exact hook, write/read a harmless report, persist `GS_RMD_PROBE_OK`,
and remain available for coordinator capture. Coordinator execution was requested
because `runSling` rejects the polecat role. No installed binary has been replaced.
Installing/restarting the daemon to activate its in-process feeder change must be
coordinated after acceptance, preserving other agents. Passing unit tests or merging
this patch alone is not evidence that installed dispatch has been repaired.
