package main

// tools_manifest.json is the contract this package publishes to the other
// clients of the same REST surface -- today the dashboard's AI assistant
// (CoreC-Dashboard, src/features/assistant/tools.ts), which cannot read Go.
// Without it the dashboard's "we are in step with corec-mcp" test compares
// against a list typed into TypeScript, so renaming a tool here would break no
// build anywhere.
//
// TestToolsManifest fails when the file and toolDefs() disagree in either
// direction. Rewrite it with:
//
//	go test ./cmd/corec-mcp -run TestToolsManifest -update
//
// Only names and the read/write split travel: descriptions are worded for the
// operator in the dashboard and the schemas are the REST shapes it already
// knows, so freezing them here would create a copy that must be kept in step
// for no gain. Names and split are what decide whether a verb exists and
// whether it can change a device.
import (
	"encoding/json"
	"flag"
	"os"
	"sort"
	"testing"
)

const manifestPath = "tools_manifest.json"

var updateManifest = flag.Bool("update", false, "rewrite "+manifestPath+" from toolDefs()")

type manifestTool struct {
	Name  string `json:"name"`
	Write bool   `json:"write"`
}

type manifest struct {
	Comment string         `json:"comment"`
	Tools   []manifestTool `json:"tools"`
}

func names(defs []map[string]any) map[string]bool {
	out := make(map[string]bool, len(defs))
	for _, d := range defs {
		if n, ok := d["name"].(string); ok {
			out[n] = true
		}
	}
	return out
}

// buildManifest reads the read/write split off toolDefs() itself rather than
// re-listing the tools: write-mode minus read-only IS the write set, so the
// manifest cannot drift from the code by forgetting a line.
func buildManifest(t *testing.T) manifest {
	t.Helper()

	readOnly := names((&server{write: false}).toolDefs())
	all := names((&server{write: true}).toolDefs())

	for n := range readOnly {
		if !all[n] {
			t.Errorf("tool %q is listed read-only but disappears when writes are enabled", n)
		}
	}

	m := manifest{
		Comment: "Generated from toolDefs() by cmd/corec-mcp/tools_manifest_test.go; " +
			"do not edit by hand. Rewrite with: go test ./cmd/corec-mcp -run TestToolsManifest -update",
		Tools: make([]manifestTool, 0, len(all)),
	}
	for n := range all {
		m.Tools = append(m.Tools, manifestTool{Name: n, Write: !readOnly[n]})
	}
	sort.Slice(m.Tools, func(i, j int) bool { return m.Tools[i].Name < m.Tools[j].Name })
	return m
}

func TestToolsManifest(t *testing.T) {
	got := buildManifest(t)

	raw, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	raw = append(raw, '\n')

	if *updateManifest {
		if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
			t.Fatalf("write %s: %v", manifestPath, err)
		}
		t.Logf("rewrote %s with %d tools", manifestPath, len(got.Tools))
		return
	}

	existing, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./cmd/corec-mcp -run TestToolsManifest -update)", manifestPath, err)
	}

	var want manifest
	if err := json.Unmarshal(existing, &want); err != nil {
		t.Fatalf("parse %s: %v", manifestPath, err)
	}

	// Compare as sets and report the members that differ: "file is stale"
	// does not say whether a verb was added, renamed or moved between
	// read and write, and that is exactly what a reviewer needs to know.
	key := func(mt manifestTool) string {
		if mt.Write {
			return mt.Name + " (write)"
		}
		return mt.Name + " (read)"
	}
	wantSet := make(map[string]bool, len(want.Tools))
	for _, mt := range want.Tools {
		wantSet[key(mt)] = true
	}
	gotSet := make(map[string]bool, len(got.Tools))
	for _, mt := range got.Tools {
		gotSet[key(mt)] = true
	}

	var missing, stale []string
	for k := range gotSet {
		if !wantSet[k] {
			missing = append(missing, k)
		}
	}
	for k := range wantSet {
		if !gotSet[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	for _, k := range missing {
		t.Errorf("%s: in toolDefs() but absent from %s", k, manifestPath)
	}
	for _, k := range stale {
		t.Errorf("%s: in %s but no longer in toolDefs()", k, manifestPath)
	}
	if len(missing) > 0 || len(stale) > 0 {
		t.Logf("to accept the current set: go test ./cmd/corec-mcp -run TestToolsManifest -update")
		t.Logf("if the dashboard lists these verbs too, update CoreC-Dashboard " +
			"src/features/assistant/tools.test.ts in the same change")
	}
}
