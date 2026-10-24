package engine

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// derivedDriverName is the synthetic driver name for derived data points.
const derivedDriverName = "derived"

// identRe matches identifiers in an expression (potential tag names).
var identRe = regexp.MustCompile(`[a-zA-Z_]\w*`)

// qualifiedRefRe matches driver-qualified tag references in an
// expression, e.g. plc1.temp in "plc1.temp * 2". Qualified refs are how
// expressions dodge the flattened tag view, which collapses same-named
// tags across drivers — an unqualified name would silently read
// whichever driver won the map race.
var qualifiedRefRe = regexp.MustCompile(`([a-zA-Z_]\w*)\.([a-zA-Z_]\w*)`)

// qualifiedRef is one driver.tag reference extracted from an expression.
type qualifiedRef struct {
	driver string
	tag    string
}

// exprBuiltins is the set of expr-lang/expr built-in function names and
// keywords to exclude when extracting referenced tag names.
var exprBuiltins = map[string]bool{
	"true": true, "false": true, "nil": true, "in": true, "not": true,
	"and": true, "or": true, "if": true, "else": true,
	"len": true, "abs": true, "ceil": true, "floor": true, "round": true,
	"int": true, "float": true, "string": true, "toString": true,
	"format": true, "trim": true, "trimPrefix": true, "trimSuffix": true,
	"upper": true, "lower": true, "split": true, "join": true,
	"repeat": true, "replace": true, "contains": true, "startsWith": true,
	"endsWith": true, "matches": true, "find": true, "type": true,
	"kind": true, "isNil": true,
}

// extractVars returns the set of unqualified tag names referenced in an
// expression, excluding built-in functions, language keywords, and the
// identifiers consumed by driver.tag qualified references (those
// resolve per-driver and must not be treated as flat tag names).
func extractVars(exprStr string) []string {
	// Blank out qualified refs first so neither their driver nor tag
	// component is scanned as a flat reference.
	blanked := qualifiedRefRe.ReplaceAllString(exprStr, "‗‗‗‗")
	matches := identRe.FindAllString(blanked, -1)
	seen := make(map[string]bool)
	var vars []string
	for _, m := range matches {
		if exprBuiltins[m] || seen[m] {
			continue
		}
		seen[m] = true
		vars = append(vars, m)
	}
	return vars
}

// extractQualifiedRefs returns the driver.tag references in an
// expression, deduplicated, in first-occurrence order.
func extractQualifiedRefs(exprStr string) []qualifiedRef {
	matches := qualifiedRefRe.FindAllStringSubmatch(exprStr, -1)
	seen := make(map[qualifiedRef]bool)
	var refs []qualifiedRef
	for _, m := range matches {
		ref := qualifiedRef{driver: m[1], tag: m[2]}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return refs
}

// compiledDerivedTag is a single derived tag with its compiled program.
type compiledDerivedTag struct {
	config     core.DerivedTagConfig
	program    *vm.Program
	vars       []string       // referenced unqualified tag names
	qualified  []qualifiedRef // driver.tag references (per-driver resolution)
	interval   time.Duration
	staleGuard time.Duration
	onMissing  onMissingPolicy
}

type onMissingPolicy int

const (
	onMissingSkip       onMissingPolicy = iota // default
	onMissingBadQuality                        // emit QualityBad point
	onMissingZero                              // treat missing as 0
)

// derivedTagEvaluator periodically evaluates derived tag expressions against
// the latest cache and pushes the results into the data bus.
//
// The evaluator owns its goroutines (per-evaluator wait group and a
// cancelable child context) instead of borrowing the engine's runtime
// wait group: reload must be able to stop and join JUST the evaluator's
// goroutines while the engine keeps running.
type derivedTagEvaluator struct {
	tags    []*compiledDerivedTag
	cache   *LatestCache
	dataBus *DataBus
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// newDerivedTagEvaluator compiles all derived tag configs. Returns nil (no
// error) when the config is empty — the engine simply has no evaluator.
func newDerivedTagEvaluator(configs []core.DerivedTagConfig, cache *LatestCache, dataBus *DataBus) (*derivedTagEvaluator, error) {
	if len(configs) == 0 {
		return &derivedTagEvaluator{}, nil
	}
	tags := make([]*compiledDerivedTag, 0, len(configs))
	for _, cfg := range configs {
		ct, err := compileDerivedTag(cfg)
		if err != nil {
			return nil, fmt.Errorf("derived tag %q: %w", cfg.Name, err)
		}
		tags = append(tags, ct)
	}
	// Cycle detection: two derived tags referencing each other would
	// evaluate silently against stale/missing cache values forever —
	// every tick computing garbage from ghosts with no signal anywhere.
	// Compile-time detection
	// fails fast with the cycle path spelled out.
	if err := detectDerivedCycles(tags); err != nil {
		return nil, err
	}
	return &derivedTagEvaluator{tags: tags, cache: cache, dataBus: dataBus}, nil
}

// detectDerivedCycles walks the derived→derived reference graph and
// errors on the first cycle found (depth-first with a visiting set).
func detectDerivedCycles(tags []*compiledDerivedTag) error {
	// A derived tag reading another derived tag references (driver
	// "derived", tag <name>) — find the referenced compiled tag and
	// recurse.
	byName := make(map[string]*compiledDerivedTag, len(tags))
	for _, t := range tags {
		byName[t.config.Name] = t
	}
	var visit func(t *compiledDerivedTag, path []string, visiting map[string]bool) error
	visit = func(t *compiledDerivedTag, path []string, visiting map[string]bool) error {
		name := t.config.Name
		if visiting[name] {
			// Splice the cycle segment out of the path for the message.
			start := 0
			for i, p := range path {
				if p == name {
					start = i
					break
				}
			}
			return fmt.Errorf("derived tags form a cycle: %s -> %s",
				strings.Join(path[start:], " -> "), name)
		}
		visiting[name] = true
		defer delete(visiting, name)
		for _, q := range t.qualified {
			if q.driver != derivedDriverName {
				continue // plain driver tag: not a derived-to-derived edge
			}
			next, ok := byName[q.tag]
			if !ok {
				continue // references a driver tag or a future tag: not a cycle
			}
			if err := visit(next, append(path, name), visiting); err != nil {
				return err
			}
		}
		// Unqualified names could also name another derived tag (the
		// flat view resolves them) — treat them as edges too.
		for _, v := range t.vars {
			if next, ok := byName[v]; ok {
				if err := visit(next, append(path, name), visiting); err != nil {
					return err
				}
			}
		}
		return nil
	}
	visiting := make(map[string]bool, len(tags))
	for _, t := range tags {
		if err := visit(t, nil, visiting); err != nil {
			return err
		}
	}
	return nil
}

func compileDerivedTag(cfg core.DerivedTagConfig) (*compiledDerivedTag, error) {
	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		return nil, fmt.Errorf("parse interval %q: %w", cfg.Interval, err)
	}
	if interval < time.Second {
		return nil, fmt.Errorf("interval %v must be >= 1s", interval)
	}
	var staleGuard time.Duration
	if cfg.StaleGuard != "" {
		staleGuard, err = time.ParseDuration(cfg.StaleGuard)
		if err != nil {
			return nil, fmt.Errorf("parse stale-guard %q: %w", cfg.StaleGuard, err)
		}
	}
	var onMissing onMissingPolicy
	switch cfg.OnMissing {
	case "", "skip":
		onMissing = onMissingSkip
	case "bad-quality":
		onMissing = onMissingBadQuality
	case "zero":
		onMissing = onMissingZero
	default:
		return nil, fmt.Errorf("invalid on-missing %q (want skip|bad-quality|zero)", cfg.OnMissing)
	}
	// Env hint is any-typed: unqualified refs resolve to float64 values,
	// while driver.tag qualified refs resolve to per-driver maps (expr
	// member access) — both evaluate dynamically at runtime.
	program, err := expr.Compile(cfg.Expr, expr.Env(map[string]any{}), expr.AllowUndefinedVariables())
	if err != nil {
		return nil, fmt.Errorf("compile expr %q: %w", cfg.Expr, err)
	}
	return &compiledDerivedTag{
		config:     cfg,
		program:    program,
		vars:       extractVars(cfg.Expr),
		qualified:  extractQualifiedRefs(cfg.Expr),
		interval:   interval,
		staleGuard: staleGuard,
		onMissing:  onMissing,
	}, nil
}

// start launches a goroutine per derived tag under a cancelable child
// of the parent (engine) context. The goroutines join the evaluator's
// own wait group so stop() can wait for exactly this evaluator's
// goroutines — the engine-wide group no longer tracks them.
func (d *derivedTagEvaluator) start(parent context.Context) {
	d.ctx, d.cancel = context.WithCancel(parent)
	for _, tag := range d.tags {
		d.wg.Add(1)
		go d.runTask(d.ctx, &d.wg, tag)
	}
}

// stop cancels the evaluator's context and waits for its goroutines to
// exit. Safe on a never-started evaluator (cancel nil, empty group).
func (d *derivedTagEvaluator) stop() {
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
}

// runTask evaluates a single derived tag on its interval until ctx is cancelled.
func (d *derivedTagEvaluator) runTask(ctx context.Context, wg *sync.WaitGroup, tag *compiledDerivedTag) {
	defer wg.Done()
	ticker := time.NewTicker(tag.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.evaluate(tag)
		}
	}
}

// evaluate computes one derived tag and pushes the result to the data bus.
func (d *derivedTagEvaluator) evaluate(tag *compiledDerivedTag) {
	snapshot := d.cache.GetAll()
	env, missing, stale := buildEnv(tag.vars, tag.qualified, snapshot, d.cache, tag.staleGuard)

	if len(missing) > 0 && tag.onMissing == onMissingSkip {
		return
	}
	// on-missing: buildEnv already zero-injects every missing ref
	// (flat and qualified), so the zero policy needs no extra work —
	// the distinction is purely that "zero" publishes a good-quality
	// point built from those zeros, while bad-quality flags it and
	// skip returns above.

	result, err := expr.Run(tag.program, env)
	if err != nil {
		slog.Debug("derived tag eval error", "tag", tag.config.Name, "error", err)
		return
	}
	val, ok := toFloat64(result)
	badValue := !ok || math.IsNaN(val) || math.IsInf(val, 0)
	if badValue {
		if tag.onMissing == onMissingSkip {
			return
		}
		val = 0
	}

	quality := core.QualityGood
	if len(missing) > 0 && tag.onMissing == onMissingBadQuality {
		quality = core.QualityBad
	}
	if badValue {
		quality = core.QualityBad
	}
	if stale {
		quality = core.QualityBad
	}

	point := core.DataPoint{
		Driver:    derivedDriverName,
		Tag:       tag.config.Name,
		Group:     tag.config.Group,
		Value:     val,
		Type:      tag.config.Type,
		Quality:   quality,
		Timestamp: time.Now(),
		IsStale:   stale,
	}
	if tag.config.Unit != "" {
		point.Metadata = map[string]string{"unit": tag.config.Unit}
	}
	d.dataBus.Push(point)
}

// buildEnv constructs the evaluation environment from the cache snapshot.
// Returns the env, the list of missing tags, and whether any tag is stale.
func buildEnv(
	vars []string,
	qualified []qualifiedRef,
	snapshot map[string]core.DataPoint,
	cache *LatestCache,
	staleGuard time.Duration,
) (env map[string]any, missing []string, stale bool) {
	env = make(map[string]any, len(vars)+len(qualified))
	now := time.Now()

	// Zero-injects keep evaluation numerically well-defined for every
	// policy: the missing list drives the on-missing policy (skip /
	// zero / bad-quality), and the env still contains an explicit 0 so
	// the expression compiles+runs identically under the any-typed env
	// hint that qualified expressions require (the legacy float64
	// typed hint let undefined variables evaluate to zero implicitly).
	injectMissing := func(name string, driver, tag string) {
		missing = append(missing, name)
		if driver == "" {
			env[name] = float64(0)
			return
		}
		driverEnv, ok := env[driver].(map[string]float64)
		if !ok {
			driverEnv = make(map[string]float64)
			env[driver] = driverEnv
		}
		driverEnv[tag] = 0
	}

	// Unqualified refs: legacy semantics against the flattened
	// snapshot (last driver with the tag name wins — the documented
	// legacy behavior; qualified refs are the fix for collisions).
	for _, v := range vars {
		dp, ok := snapshot[v]
		if !ok {
			injectMissing(v, "", "")
			continue
		}
		f, okNum := toFloat64(dp.Value)
		if !okNum {
			injectMissing(v, "", "")
			continue
		}
		env[v] = f
		if staleGuard > 0 && now.Sub(dp.Timestamp) > staleGuard {
			stale = true
		}
	}

	// Qualified refs: resolve the exact (driver, tag)
	// pair through the cache, so same-named tags across drivers stay
	// distinct. expr evaluates them as member access on per-driver
	// maps: env[driver] = {tag: value}.
	for _, q := range qualified {
		dp, ok := cache.Get(q.driver, q.tag)
		if !ok {
			injectMissing(q.driver+"."+q.tag, q.driver, q.tag)
			continue
		}
		f, okNum := toFloat64(dp.Value)
		if !okNum {
			injectMissing(q.driver+"."+q.tag, q.driver, q.tag)
			continue
		}
		driverEnv, ok := env[q.driver].(map[string]float64)
		if !ok {
			driverEnv = make(map[string]float64)
			env[q.driver] = driverEnv
		}
		driverEnv[q.tag] = f
		if staleGuard > 0 && now.Sub(dp.Timestamp) > staleGuard {
			stale = true
		}
	}
	return env, missing, stale
}

// toFloat64 converts a numeric any to float64. Returns false for non-numbers.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// validateDerivedTags checks config-level invariants: unique names,
// non-empty expr, valid interval. Per-tag compilation errors are caught
// in newDerivedTagEvaluator.
func validateDerivedTags(configs []core.DerivedTagConfig) error {
	names := make(map[string]bool)
	for _, cfg := range configs {
		if strings.TrimSpace(cfg.Name) == "" {
			return fmt.Errorf("derived tag has empty name")
		}
		if names[cfg.Name] {
			return fmt.Errorf("duplicate derived tag name %q", cfg.Name)
		}
		names[cfg.Name] = true
		if strings.TrimSpace(cfg.Expr) == "" {
			return fmt.Errorf("derived tag %q has empty expr", cfg.Name)
		}
		if strings.TrimSpace(cfg.Interval) == "" {
			return fmt.Errorf("derived tag %q has empty interval", cfg.Name)
		}
	}
	return nil
}
