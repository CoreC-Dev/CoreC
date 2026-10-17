package engine

import (
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

func (e *CoreCEngine) LatestValues(driver string) map[string]core.DataPoint {
	return e.reads.LatestValues(driver)
}

func (e *CoreCEngine) StaleThreshold() time.Duration {
	return e.reads.StaleThreshold()
}
