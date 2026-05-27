// Package pyroscope is a continuous-profiling adapter for Grafana
// Pyroscope (and Pyroscope-compatible backends like Grafana Phlare).
// Wraps the pyroscope-go runtime as a lifecycle component so the
// profiler starts on service start and stops cleanly on SIGTERM.
//
// # Use this package when
//
//   - You want flame-graphs of CPU / allocations / inuse memory for a
//     long-running service, sampled continuously, viewable in Grafana.
//   - You are willing to accept ~1-2% CPU overhead in exchange for
//     production-quality always-on profiling.
//
// # Do NOT use this package for
//
//   - Ad-hoc profiling. Use the kit's [observability/pprof] endpoint
//     and `go tool pprof` directly; cheaper than maintaining a
//     profile-upload pipeline.
//   - Tracing. Use [observability/tracing] (OpenTelemetry) — profiles
//     and traces are complementary, not substitutes.
//
// # Sibling packages
//
//   - [observability/pprof]    — on-demand pprof endpoint (no upload)
//   - [observability/tracing]  — OTel traces (different signal entirely)
//   - [observability/redmetrics] — RED metrics (the third signal)
//
// # Quick start
//
//	cmp, err := pyroscope.Component(pyroscope.Config{
//	    ServerAddress: "http://pyroscope:4040",
//	    AppName:       "my-service",
//	    Tags:          map[string]string{"env": "prod"},
//	})
//	if err != nil { return err }
//	runner.Add("pyroscope", cmp)  // any lifecycle.Runner
//
// As an app.Module:
//
//	app.New(name, ver, cfg).With(pyroscope.Module(pyroscope.Config{...})).Run()
//
// # Operational cost
//
// Pyroscope-go takes a CPU profile sample every 10ms by default and
// uploads aggregated profiles on a UploadRate ticker (default 15s).
// Production observed cost: 0.5-1.5% CPU. Tag cardinality matters —
// tag by `env`, `version`, `region`, NEVER by user-ID or request-ID.
package pyroscope
