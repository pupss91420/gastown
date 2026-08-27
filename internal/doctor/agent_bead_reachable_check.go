package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
)

// AgentBeadReachableCheck verifies that every agent can reach its own agent bead
// from its own working directory.
//
// agent-beads-exist only asserts that each agent bead exists *somewhere* — it
// queries the town and rig databases directly, so it stays green even when no
// agent can actually read its own bead from its own cwd. That blind spot is how
// hq-5z6 stayed hidden: agent beads carry a rig prefix but are created in the
// town database, while a rig agent's cwd resolves to the rig database, so idle
// counters, backoff windows, and heartbeats silently failed to persist.
//
// gt now falls back to the town database for agent-bead operations (see
// cmd.resolveAgentBeadDir), which makes the reachable set for an agent "the
// database its cwd resolves to, plus town". This check asserts that invariant
// per agent so a future routing regression is caught here instead of in a
// witness log nobody reads.
type AgentBeadReachableCheck struct {
	BaseCheck
}

// NewAgentBeadReachableCheck creates a new agent bead reachability check.
func NewAgentBeadReachableCheck() *AgentBeadReachableCheck {
	return &AgentBeadReachableCheck{
		BaseCheck: BaseCheck{
			CheckName:        "agent-bead-reachable",
			CheckDescription: "Verify each agent can read its own agent bead from its own directory",
			CheckCategory:    CategoryRig,
		},
	}
}

// agentBeadLocation pairs an agent bead ID with the directory the owning agent
// runs from.
type agentBeadLocation struct {
	id       string
	agentDir string // absolute path to the agent's working directory
}

// Run reports agents whose agent bead is unreachable from their own directory.
func (c *AgentBeadReachableCheck) Run(ctx *CheckContext) *CheckResult {
	townBeadsDir := beads.ResolveBeadsDir(beads.GetTownBeadsPath(ctx.TownRoot))

	routes, err := beads.LoadRoutes(filepath.Join(ctx.TownRoot, ".beads"))
	if err != nil {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusWarning,
			Message: "Could not load routes.jsonl",
		}
	}

	locations := agentBeadLocations(ctx, routes)
	if len(locations) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "No agent directories to check",
		}
	}

	// Cache bead IDs per database — several agents share one resolved database.
	idsByDir := make(map[string]map[string]bool)
	beadIDsIn := func(beadsDir string) map[string]bool {
		if beadsDir == "" {
			return nil
		}
		if ids, ok := idsByDir[beadsDir]; ok {
			return ids
		}
		ids := listBeadIDs(beadsDir)
		idsByDir[beadsDir] = ids
		return ids
	}

	var unreachable []string
	checked := 0
	for _, loc := range locations {
		resolved := resolveBeadsDirFrom(loc.agentDir)
		if resolved == "" {
			continue // Agent dir is outside any beads workspace — nothing to assert.
		}
		checked++
		if beadIDsIn(resolved)[loc.id] || beadIDsIn(townBeadsDir)[loc.id] {
			continue
		}
		rel, relErr := filepath.Rel(ctx.TownRoot, loc.agentDir)
		if relErr != nil {
			rel = loc.agentDir
		}
		unreachable = append(unreachable, fmt.Sprintf("%s: %s resolves to %s", loc.id, rel, resolved))
	}

	if len(unreachable) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: fmt.Sprintf("All %d agent bead(s) reachable from their agent's directory", checked),
		}
	}

	sort.Strings(unreachable)
	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("%d agent bead(s) unreachable from the owning agent's directory", len(unreachable)),
		Details: unreachable,
		FixHint: "Run 'gt doctor --fix' to create missing agent beads, then re-check routing (agent beads live in the town database)",
	}
}

// agentBeadLocations enumerates the agent beads expected for on-disk agents,
// paired with the directory each agent runs from. Agents whose directory does
// not exist are skipped: they are not running, so they cannot be broken.
func agentBeadLocations(ctx *CheckContext, routes []beads.Route) []agentBeadLocation {
	var locations []agentBeadLocation

	add := func(id, agentDir string) {
		if id == "" {
			return
		}
		if _, err := os.Stat(agentDir); err != nil {
			return
		}
		locations = append(locations, agentBeadLocation{id: id, agentDir: agentDir})
	}

	// Town-level agents run from the town root.
	if ctx.RigName == "" {
		add(beads.MayorBeadIDTown(), filepath.Join(ctx.TownRoot, "mayor"))
		add(beads.DeaconBeadIDTown(), filepath.Join(ctx.TownRoot, "deacon"))
	}

	for _, r := range routes {
		parts := strings.Split(r.Path, "/")
		if len(parts) == 0 || parts[0] == "." {
			continue
		}
		rigName := parts[0]
		if ctx.RigName != "" && rigName != ctx.RigName {
			continue
		}
		prefix := strings.TrimSuffix(r.Prefix, "-")
		rigDir := filepath.Join(ctx.TownRoot, rigName)

		add(beads.WitnessBeadIDWithPrefix(prefix, rigName), filepath.Join(rigDir, "witness"))
		add(beads.RefineryBeadIDWithPrefix(prefix, rigName), filepath.Join(rigDir, "refinery"))

		for _, worker := range listCrewWorkers(ctx.TownRoot, rigName) {
			add(beads.CrewBeadIDWithPrefix(prefix, rigName, worker), filepath.Join(rigDir, "crew", worker))
		}
		for _, polecat := range listPolecats(ctx.TownRoot, rigName) {
			add(beads.PolecatBeadIDWithPrefix(prefix, rigName, polecat), filepath.Join(rigDir, "polecats", polecat))
		}
	}

	return locations
}

// resolveBeadsDirFrom mirrors how gt resolves a beads database from an agent's
// working directory: walk up to the nearest .beads, then follow redirects.
// Returns "" when no .beads directory is found.
func resolveBeadsDirFrom(startDir string) string {
	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, ".beads")); err == nil {
			return beads.ResolveBeadsDir(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// listBeadIDs returns the set of agent bead IDs readable from beadsDir,
// covering both the issues and wisps tables.
func listBeadIDs(beadsDir string) map[string]bool {
	bd := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
	ids := make(map[string]bool)
	if agents, err := bd.ListAgentBeads(); err == nil {
		for id := range agents {
			ids[id] = true
		}
	}
	if wisps, _ := bd.ListWispIDs(); wisps != nil {
		for id := range wisps {
			ids[id] = true
		}
	}
	return ids
}
