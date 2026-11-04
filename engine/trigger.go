package engine

import (
	"context"
	"log/slog"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
)

// gateTriggerTags decides which tags this cycle should read when the task
// carries trigger groups. Trigger tags are ALWAYS included (their fresh value
// is what the rule engine needs to see), and a group's gated tags are added
// only when the group's condition currently evaluates true.
//
// On a condition read error the group is suppressed (fail-closed): a broken
// flag must not turn into a flood of detail reads. The suppression is counted
// per group and logged at debug level, since it is normal for a flag to be
// unreachable while the device is idle.
func (r *taskRunner) gateTriggerTags(ctx context.Context, task core.ScheduleTask,
	read func(context.Context, string, []string) ([]core.TagValue, error)) ([]string, []core.TagValue) {
	if len(r.groupTrigger) == 0 {
		return task.Tags, nil
	}

	// Read all conditions in one round-trip; they live on the same device.
	conditions := make([]string, 0, len(r.groupTrigger))
	for _, tag := range r.groupTrigger {
		conditions = append(conditions, tag)
	}
	condValues, err := read(ctx, task.Driver, conditions)
	if err != nil {
		slog.Debug("trigger read failed, all groups suppressed",
			"driver", task.Driver, "error", err)
		// One tick counts once, not once per group: the counter answers
		// "how many collection cycles skipped detail reads", so N groups
		// on one device do not inflate it. Nothing is left to read --
		// condValues is empty, the condition itself failed.
		r.skipped.Add(1)
		return nil, condValues
	}

	fired := make(map[string]bool, len(condValues))
	for _, v := range condValues {
		fired[v.Tag] = triggerFired(v)
	}

	// Only gated detail points are left for the caller to read: the
	// conditions themselves were just read above (condValues) and must not be
	// requested twice in one cycle. A gated tag is added when ITS group's
	// condition fired; adding everything because one group fired would drag
	// unrelated details along.
	var tags []string
	suppressed := false
	for _, tag := range task.Tags {
		group, gated := r.gatedOf(tag)
		if !gated {
			continue
		}
		if fired[r.groupTrigger[group]] {
			tags = append(tags, tag)
		} else {
			suppressed = true
		}
	}
	// Counted per cycle (not per suppressed tag) for the same reason as above.
	if suppressed {
		r.skipped.Add(1)
	}
	// The condition values are returned so the caller publishes them in this
	// cycle too: they were just read and belong to the same sample batch.
	return tags, condValues
}

// gatedOf reports whether tag is gated by a trigger group, and which one.
func (r *taskRunner) gatedOf(tag string) (string, bool) {
	for group, tags := range r.gated {
		for _, g := range tags {
			if g == tag {
				return group, true
			}
		}
	}
	return "", false
}

// triggerFired reports whether a condition's value means "collect the
// details": quality-good AND (true for bool / non-zero for numeric). Any
// other quality or a non-numeric type suppresses the group -- fail-closed.
func triggerFired(v core.TagValue) bool {
	if v.Quality != core.QualityGood {
		return false
	}
	switch val := v.Value.(type) {
	case bool:
		return val
	default:
		f, ok := util.ToFloat64OK(v.Value)
		return ok && f != 0
	}
}
