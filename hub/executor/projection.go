package executor

import (
	"slices"

	"github.com/CoreC-Dev/CoreC/core"
)

// cloneForMutation copies the top-level slices so a failed reload cannot
// write through to the live config. The element structs are values; the
// slices are what removeNamed* splices in place.
func cloneForMutation(cfg *core.Config) *core.Config {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.Drivers = slices.Clone(cfg.Drivers)
	out.Transports = slices.Clone(cfg.Transports)
	out.Rules = slices.Clone(cfg.Rules)
	return &out
}

// sourceProjection returns a copy of cfg suitable for writing back to the
// source YAML. A driver with tags-file keeps only its authored inline tags:
// loadTagsFiles re-expands the file on the next load, so persisting the
// expanded list would duplicate every file tag.
//
// InlineTags is populated by loadTagsFiles. A config built in code without
// going through Parse/Load has no projection to recover, so its tags are
// emitted verbatim.
func sourceProjection(cfg *core.Config) *core.Config {
	if cfg == nil {
		return nil
	}
	out := *cfg
	out.Drivers = slices.Clone(cfg.Drivers)
	for i := range out.Drivers {
		if out.Drivers[i].TagsFile != "" && out.Drivers[i].InlineTags != nil {
			out.Drivers[i].Tags = out.Drivers[i].InlineTags
		}
	}
	return &out
}
