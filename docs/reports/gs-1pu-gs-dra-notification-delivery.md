# Notification delivery repair: gs-1pu and gs-dra

Scope: the gs-1pu hook explicitly assigns gs-dra as its companion. Based on
`pupss91420/gastown` origin/main at `c49fe986`, for the local fork Refinery MQ.

## Changes

Every successful shared tmux startup registers the existing nudge poller,
including mayor and boot. Polecats have a separate startup path and now register
it too. This does not rely on `.claude/settings.json` or mistake Codex's prompt
detection for a turn-boundary drain hook. Starting an already-running tmux mayor
repairs a missing poller; queue-mode nudges and wait-idle queue fallbacks also
ensure a consumer. If poller startup fails, enqueue returns an explicit error
while retaining the durable queued item. Reuse requires an owned process identity
and matching ready record. A private
launch token binds the process arguments, recipient session, PID record and child
readiness acknowledgement; early child exit fails startup. Bare/reused PIDs and
mismatched session/token records never count as ready or authorize signaling.
Agent startup treats drain failure as a visible warning after session creation,
so callers do not retry ownership already held by a live agent. Existing-session
repair preserves the already-running sentinel.
ACP retains its Propeller. Mayor/polecat stop paths stop their pollers; other
shared-start roles retain existing teardown or poller exit on session death.
Poller injection uses the town's cross-process nudge lock and requeues on failure.

Refinery and witness events now use `events/<channel>/<rig>/`. Their internal
MQ_SUBMIT, POLECAT_DONE, and MERGE_READY producers record the recipient rig in
the payload. Both CLI event commands resolve the same scope: explicit `--rig`,
current rig directory, then `GT_RIG`. Missing/invalid recipient scope fails.
The daemon's refinery event gate checks that same rig directory. Patrol formulas
specify the recipient. Mayor and custom channels remain town-wide.

Cleanup deletes only returned files in the selected rig directory. Before
consumption or the daemon event gate, a compatibility pass atomically moves
legacy flat files whose valid event channel and payload.rig identify this rig.
A channel migration lock prevents concurrent movers from duplicating delivery;
unexpected destination collisions fail without overwriting either file. The
bytes survive unchanged, and each source transfers once. There is no global
migration-complete marker, so late emissions during a rolling upgrade can still
be routed on the next consumer/gate pass. Malformed, ambiguous and conflicting
channel events remain untouched for owner reconciliation. Old running consumers
must be coordinated during deployment because old binaries still read flat files.

## Verification

Run `python3 scripts/test-notification-delivery.py` from this checkout.
The runner removes inherited GT/BD/BEADS/DOLT/agent settings, uses a temporary
HOME/town and nonproduction Dolt port 1, and selects tests whose filesystem,
terminal and database boundaries are isolated. It does not run the full suite.

- Codex startup with a stale Claude UserPromptSubmit declaration still registers
  its poller; failed session creation does not register one.
- Real queue files reach an actual shell reader via an isolated tmux socket with
  a synthetic Codex prompt. The terminal records the marker and the queue empties.
  This proves terminal delivery, not live model acknowledgement.
- Codex/Claude/Gemini delivery boundaries exercise idle deferral, injection error,
  successful retry, no duplicate delivery, and preservation of another session.
- Actual sling/done producers create events for two rigs; actual await-event
  cleanup consumes each rig separately, leaving the other rig and flat legacy
  ambiguous files untouched. Attributable legacy files route once alongside scoped
  events, including actual await-event cleanup. Concurrent compatibility passes
  preserve one byte-identical copy and do not move another rig's source. CLI
  emission respects an explicit recipient over caller rig.
- Witness MERGE_READY emission and daemon event-gate isolation pass, including
  a pre-upgrade attributable event opening only its recipient rig's gate.
- Durable enqueue reports a real poller PID-directory startup failure and keeps
  the message. The actual CLI starts a ready poller and reuses it without a duplicate.
  Real poller startup/readiness and reuse pass. Bare live PIDs, forged ready
  records, wrong sessions and early child exit are rejected. Stop refuses to
  signal mismatched processes. Mayor and polecat startup remain successful when
  only poller startup fails; a retry recognizes the existing session and the
  terminal fixture records exactly one creation.
- Channel, nudge, session and mayor package tests, selected event/poller tests,
  CLI build, and vet for all changed Go packages pass.

## Preserved evidence and deployment boundary

At 2026-09-07T17:27:34Z, 79 live queue/channel evidence files were copied,
not moved or drained. The bytes and private manifest are preserved outside this
repository in the Mayor's existing evidence area. No production payload, config,
archive, manifest, or absolute host path is published in this report/branch.
Private evidence manifest SHA-256: `9e532ca568a81504204ccf2139fa7c976828c25cd508657b3b91325890663492`.
Notification expiry does not imply durable mail messages were lost.

No installed binary, running daemon, existing agent session, global configuration,
remote, AlphaPrime audit, or real respawn counter was changed. The built test
binary stays inside `.runtime/notification-checks`. Mayor owns integration,
deployment, legacy-event reconciliation and live acceptance. Both assigned code
repairs are included; live acceptance is not claimed.
