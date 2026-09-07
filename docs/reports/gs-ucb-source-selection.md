# gs-ucb: binary freshness source selection

The comparison now uses build provenance before town layout. `make build`
stamps the Git common directory and symbolic build ref into the binary. The
compiler source path supplies a fallback for ordinary local Go builds. A removed
build worktree can still be checked through its surviving shared repository.

Without surviving provenance, discovery includes the town's `.repo.git`, the
mayor clone, development paths, and the current checkout. Candidates must contain
the binary commit and its committed `cmd/gt/main.go`. Worktrees and symlink aliases
are deduplicated by Git common directory; independent matching clones are
ambiguous even when their URLs match. Missing or ambiguous evidence is reported
as undetermined. Discovery performs only local reads, without fetching.

A recorded build ref remains authoritative when a source worktree switches
branches. Otherwise, build-branch tracking configuration binds remote candidates,
including custom remote names. Divergent unbound candidates are skipped. Bare
repositories support freshness comparisons but never authorize worktree rebuilds.
Text and JSON expose the selected repository, common directory, fully qualified
ref, and its configured remote URL. Untracked local refs have no inferred remote.

Validation passed:

- `go test ./internal/version -count=1`
- `go test ./internal/cmd -run '^(TestOutputStaleText|TestStaleQuietExitCode|TestStaleRepositoryEvidence)$' -count=1`
- `go vet ./internal/version ./internal/cmd`
- `make build BUILD_DIR=./.build-gs-ucb`
- `git diff --check`

The Git fixtures reproduce the separate upstream mayor clone and shared bare
fork, removed and switched worktrees, independent clones with identical URLs,
missing provenance/commits/source markers, symlink/common-directory aliases, and
custom tracking remotes alongside divergent upstream history. Output tests call
pure renderers. No tests execute production Dolt, event, or nudge operations.

This does not migrate rig configuration or alter remotes, installed binaries,
services, counters, or AlphaPrime. Deployment remains with the mayor. Existing
binaries gain this behavior only after an authorized rebuild. Relocated or
trimpath builds without usable provenance may conservatively report undetermined.
