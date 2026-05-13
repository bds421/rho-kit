// Package clamav implements uploadsec.Scanner using the ClamAV clamd
// INSTREAM protocol.
//
// The package speaks the clamd wire protocol directly so importing the
// adapter does not pull a large SDK into services that only need upload
// validation.
//
// # Observability
//
// Prometheus metrics are opt-in via WithMetrics. Two collectors are
// exported:
//
//   - clamav_scan_duration_seconds{validator}    histogram of scan
//     latency covering dial + INSTREAM exchange + response read.
//   - clamav_scans_total{validator,outcome}      total scans split by
//     outcome: "clean", "infected", "error". The infected vs. error
//     split lets operators alert separately — "error" is a clamd
//     outage failing closed, "infected" can be an upload attack.
//
// The "validator" label defaults to "clamav"; override with
// WithMetricsValidatorName when several scanners run side-by-side.
package clamav
