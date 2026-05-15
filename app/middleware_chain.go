package app

import (
	"net/http"
	"sort"
)

// applyPhasedMiddleware composes middleware contributed by every
// module implementing [MiddlewareInstaller], in stable phase order.
//
// Phases run outermost-first (highest phase first wraps the rest),
// matching the kit's inside-out composition convention. Within a
// single phase, modules wrap in REGISTRATION order — meaning the
// LAST-registered module at a given phase ends up OUTERMOST. This
// matches how httpx.Stack composes manual middleware lists too.
//
// Returns handler unwrapped if no module installs middleware.
func applyPhasedMiddleware(handler http.Handler, modules []Module) http.Handler {
	type entry struct {
		idx int // registration index, breaks ties within a phase
		PhasedMiddleware
	}
	var collected []entry
	for i, m := range modules {
		mi, ok := m.(MiddlewareInstaller)
		if !ok {
			continue
		}
		for _, pm := range mi.PublicMiddleware() {
			if pm.Func == nil {
				continue
			}
			collected = append(collected, entry{idx: i, PhasedMiddleware: pm})
		}
	}
	if len(collected) == 0 {
		return handler
	}

	// Sort ascending by phase, then by registration index. Then
	// apply inside-out (lowest phase wraps first → ends up
	// innermost). This means the highest-phase, latest-registered
	// middleware is the OUTERMOST in the final chain.
	sort.SliceStable(collected, func(i, j int) bool {
		if collected[i].Phase != collected[j].Phase {
			return collected[i].Phase < collected[j].Phase
		}
		return collected[i].idx < collected[j].idx
	})

	for _, e := range collected {
		handler = e.Func(handler)
	}
	return handler
}
