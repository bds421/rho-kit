// Package openapifilter serves part of an OpenAPI contract: the
// operations whose value of one extension (such as x-profile) a run
// keeps. The document it leaves drops the removed operations, the paths
// left without one, and the tags and components only they referred to;
// Routes registers only the kept operations on a mux and lists those no
// handler registered, so a contract and its generated server out of step
// stop the start. It owns the kin-openapi dependency so other modules stay
// free of it.
package openapifilter
