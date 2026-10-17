package engine

import (
	"context"

	"github.com/CoreC-Dev/CoreC/core"
)

// --- Data Operations ---

func (e *CoreCEngine) ReadTag(ctx context.Context, driver, tag string) (*core.TagValue, error) {
	return e.reads.ReadTag(ctx, driver, tag)
}
