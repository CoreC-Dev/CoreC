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

// extractVars returns the set of tag names referenced in an expression,
// excluding built-in functions and language keywords.
func extractVars(exprStr string) []string {
	matches := identRe.FindAllString(exprStr, -1)
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

// compiledDerivedTag is a single derived tag with its compiled program.
type compiledDerivedTag struct {
	config     core.DerivedTagConfig
	program    *vm.Program
	vars       []string // referenced tag names
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
// goroutines while the engine keeps running (audit finding CFG-1 —
// derived-tags changes were previously silently ignored on reload).
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
	return &derivedTagEvaluator{tags: tags, cache: cache, dataBus: dataBus}, nil
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
	program, err := expr.Compile(cfg.Expr, expr.Env(map[string]float64{}), expr.AllowUndefinedVariables())
	if err != nil {
		return nil, fmt.Errorf("compile expr %q: %w", cfg.Expr, err)
	}
	return &compiledDerivedTag{
		config:     cfg,
		program:    program,
		vars:       extractVars(cfg.Expr),
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
	env, missing, stale := buildEnv(tag.vars, snapshot, tag.staleGuard)

	if len(missing) > 0 && tag.onMissing == onMissingSkip {
		return
	}
	if len(missing) > 0 && tag.onMissing == onMissingZero {
		for _, m := range missing {
			env[m] = 0
		}
	}

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
func buildEnv(vars []string, snapshot map[string]core.DataPoint, staleGuard time.Duration) (env map[string]float64, missing []string, stale bool) {
	env = make(map[string]float64, len(vars))
	now := time.Now()
	for _, v := range vars {
		dp, ok := snapshot[v]
		if !ok {
			missing = append(missing, v)
			continue
		}
		f, ok := toFloat64(dp.Value)
		if !ok {
			missing = append(missing, v)
			continue
		}
		env[v] = f
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
