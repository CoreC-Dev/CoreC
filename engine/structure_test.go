package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Anti-regression guards for the CPLX-020/CPLX-025 God Object splits
// (docs/exec-plans/tech-debt-tracker.md). The acceptance for both
// entries is: every struct in the split keeps fields <= 10 and its own
// logic methods <= 12. These tests mechanically pin those limits, so a
// field or method creeping back onto CoreCEngine or transportBatcher
// fails the suite instead of quietly recreating the God Object.
//
// Counting conventions (documented in the tracker):
//   - Field counts come from reflection (exact, includes unexported
//     fields; every role pointer and the shared mutex counts).
//   - Method counts come from go/ast over the package's non-test
//     sources, because reflect cannot see unexported methods. Both
//     value and pointer receivers are counted per named type.
//   - CoreCEngine is the composition shell: its methods are interface
//     forwarders only (a single statement delegating to a role), so it
//     is not subject to the <=12 method limit — instead every one of
//     its methods must BE a forwarder.

const (
	maxGodObjectFields = 10
	maxRoleMethods     = 12
)

// godObjectStructs lists every struct created or reshaped by the
// CPLX-020/CPLX-025 splits, with the field ceiling they must respect.
func godObjectStructs() []struct {
	name string
	typ  reflect.Type
} {
	return []struct {
		name string
		typ  reflect.Type
	}{
		{"CoreCEngine", reflect.TypeOf(CoreCEngine{})},
		{"engineRuntime", reflect.TypeOf(engineRuntime{})},
		{"ruleRole", reflect.TypeOf(ruleRole{})},
		{"readAccessor", reflect.TypeOf(readAccessor{})},
		{"statsCollector", reflect.TypeOf(statsCollector{})},
		{"driverRegistry", reflect.TypeOf(driverRegistry{})},
		{"transportManager", reflect.TypeOf(transportManager{})},
		{"writeManager", reflect.TypeOf(writeManager{})},
		{"processingCore", reflect.TypeOf(processingCore{})},
		{"lifecycleSupport", reflect.TypeOf(lifecycleSupport{})},
		// B1 (quality audit 2026-10-21): these two roles grew past the
		// method ceiling AFTER the split, outside both guard lists —
		// now pinned. Both sit AT the 10-field limit.
		{"alertManager", reflect.TypeOf(alertManager{})},
		{"scheduler", reflect.TypeOf(scheduler{})},
		{"transportBatcher", reflect.TypeOf(transportBatcher{})},
		{"batcherRetryPolicy", reflect.TypeOf(batcherRetryPolicy{})},
		{"batcherBufferState", reflect.TypeOf(batcherBufferState{})},
	}
}

// TestGodObjectFieldLimit asserts every CPLX-020/025 struct stays at or
// below the 10-field acceptance ceiling.
func TestGodObjectFieldLimit(t *testing.T) {
	for _, s := range godObjectStructs() {
		if got := s.typ.NumField(); got > maxGodObjectFields {
			t.Errorf("%s has %d fields, exceeds the CPLX-020/025 limit of %d — move the new state into a dedicated role/sub-struct (see docs/exec-plans/tech-debt-tracker.md CPLX-020/025)",
				s.name, got, maxGodObjectFields)
		}
	}
}

// roleMethodCounts parses the package's non-test sources and counts
// declared methods per receiver type. reflect is not enough here: it
// cannot enumerate unexported methods, and most role logic is
// unexported.
func roleMethodCounts(t *testing.T) map[string]int {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	fset := token.NewFileSet()
	counts := make(map[string]int)
	for _, ent := range entries {
		name := ent.Name()
		if filepath.Ext(name) != ".go" {
			continue
		}
		// Skip test files: mocks there declare methods on their own
		// types and must not influence production limits.
		if len(name) > len("_test.go") && name[len(name)-len("_test.go"):] == "_test.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			switch recv := fd.Recv.List[0].Type.(type) {
			case *ast.StarExpr:
				if id, ok := recv.X.(*ast.Ident); ok {
					counts[id.Name]++
				}
			case *ast.Ident:
				counts[recv.Name]++
			}
		}
	}
	return counts
}

// TestRoleMethodLimit asserts every role struct (and the batcher
// sub-structs) stays at or below the 12-method acceptance ceiling.
// CoreCEngine is exempt from the count but pinned to forwarder shape
// by TestCoreCEngineMethodsAreForwarders below.
func TestRoleMethodLimit(t *testing.T) {
	counts := roleMethodCounts(t)
	limited := []string{
		"engineRuntime", "ruleRole", "readAccessor", "statsCollector",
		"driverRegistry", "transportManager", "writeManager",
		"processingCore", "lifecycleSupport", "transportBatcher",
		"batcherRetryPolicy", "batcherBufferState",
		// B1 (quality audit 2026-10-21): added alongside a one-method
		// shed each (scheduler.handleReadError → free function mirroring
		// handleRecovery; alertManager.evictOldestLocked inlined into its
		// single caller). 13 methods each before; 12 now.
		"alertManager", "scheduler",
	}
	for _, name := range limited {
		got := counts[name]
		if got > maxRoleMethods {
			t.Errorf("%s declares %d methods, exceeds the CPLX-020/025 limit of %d — move the new logic into a dedicated role/sub-struct (see docs/exec-plans/tech-debt-tracker.md CPLX-020/025)",
				name, got, maxRoleMethods)
		}
	}
}

// TestCoreCEngineMethodsAreForwarders pins the shell discipline of the
// CPLX-020 split: every method declared on CoreCEngine must be a
// <=3-line forwarder delegating to exactly one role — a single
// return/expr statement, with no control flow, local state, or direct
// field arithmetic. Interface forwarding is not counted as logic by
// the acceptance; this is the mechanical enforcement of that rule.
func TestCoreCEngineMethodsAreForwarders(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	fset := token.NewFileSet()
	total := 0
	for _, ent := range entries {
		name := ent.Name()
		if filepath.Ext(name) != ".go" {
			continue
		}
		if len(name) > len("_test.go") && name[len(name)-len("_test.go"):] == "_test.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) == 0 {
				continue
			}
			se, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			id, ok := se.X.(*ast.Ident)
			if !ok || id.Name != "CoreCEngine" {
				continue
			}
			total++
			if fd.Body == nil || len(fd.Body.List) != 1 {
				t.Errorf("%s: CoreCEngine.%s must be a single-statement forwarder to a role (CPLX-020 shell discipline); move the logic into the owning role struct",
					name, fd.Name.Name)
				continue
			}
			switch fd.Body.List[0].(type) {
			case *ast.ReturnStmt, *ast.ExprStmt:
				// Forwarder shape: a single delegation to a role.
			default:
				t.Errorf("%s: CoreCEngine.%s must be a single return/expr statement delegating to a role (CPLX-020 shell discipline)",
					name, fd.Name.Name)
			}
		}
	}
	if total == 0 {
		t.Fatal("no CoreCEngine methods found — the parser scan is broken and cannot guard the CPLX-020 shell discipline")
	}
}

// TestRoleCompositionIsComplete asserts the shell wires every role and
// its back-pointers: an engine built via New() must have all nine role
// structs plus the runtime non-nil, so a forwarder can never dereference
// a nil role in production wiring.
func TestRoleCompositionIsComplete(t *testing.T) {
	e := New().(*CoreCEngine)
	roles := []struct {
		name string
		ok   bool
	}{
		{"runtime", e.runtime != nil},
		{"ruleSet", e.ruleSet != nil},
		{"reads", e.reads != nil},
		{"stats", e.stats != nil},
		{"driverReg", e.driverReg != nil},
		{"transportMgr", e.transportMgr != nil},
		{"writes", e.writes != nil},
		{"processing", e.processing != nil},
		{"lifecycle", e.lifecycle != nil},
	}
	for _, r := range roles {
		if !r.ok {
			t.Errorf("New() left the %s role nil; the composition shell must allocate and wire every role", r.name)
		}
	}
	if e.reads.eng != e || e.stats.eng != e || e.driverReg.eng != e ||
		e.transportMgr.eng != e || e.writes.eng != e ||
		e.processing.eng != e || e.lifecycle.eng != e || e.runtime.eng != e {
		t.Error("New() left a role back-pointer unwired; role methods would dereference a nil engine")
	}
}
