// Package bulkhead bounds the number of concurrent operations
// against a single downstream so a slow / wedged downstream cannot
// exhaust the service's goroutine + connection budget. The
// canonical pairing is:
//
//	circuitbreaker  →  bulkhead  →  retry  →  call
//	(fast-fail when    (cap concurrent  (transient   (real
//	 downstream broken) in-flight)       blip)        downstream)
//
// Bulkhead sits between breaker and retry because:
//
//   - The breaker rejects fast when the downstream is confirmed
//     broken, so the bulkhead's slots aren't filled with doomed
//     attempts.
//
//   - The retry policy may multiply attempts per logical request.
//     Putting bulkhead BEFORE retry caps total concurrent
//     attempts; putting it AFTER would cap only the first-attempt
//     concurrency and let retries spike through.
//
// The kit's bulkhead is a counting semaphore with three rejection
// modes: ctx-cancel-on-wait (the default), immediate rejection
// (when MaxQueueWait is zero), or queued-then-cancel after the
// configured wait. Wait timeouts are observable via the
// `bulkhead_acquisitions_total{outcome=...}` Prometheus metric.
//
// Bulkhead is a per-downstream primitive — construct one per
// distinct downstream resource (database pool, downstream HTTP
// service, broker) rather than a single global. A global bulkhead
// would couple unrelated downstreams' failure modes.
package bulkhead
