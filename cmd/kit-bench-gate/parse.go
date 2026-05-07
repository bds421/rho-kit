package main

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// Result is one parsed benchmark line: name + per-op metrics.
type Result struct {
	Name       string
	NsPerOp    float64 // ns/op
	BPerOp     int64   // B/op
	AllocsOp   int64   // allocs/op
	Iterations int64
}

// Parse reads `go test -bench` text output and returns one Result per
// benchmark line. Lines without `ns/op` are silently skipped so the
// caller can pipe in raw `go test` output without pre-filtering.
func Parse(r io.Reader) ([]Result, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out []Result
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// Expect: <name> <iters> <ns> ns/op [<B> B/op] [<allocs> allocs/op]
		res := Result{Name: stripGoroutineSuffix(fields[0])}
		if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			res.Iterations = n
		}
		// Walk pairs of (value, unit).
		for i := 2; i+1 < len(fields); i += 2 {
			val := fields[i]
			unit := fields[i+1]
			switch unit {
			case "ns/op":
				if v, err := strconv.ParseFloat(val, 64); err == nil {
					res.NsPerOp = v
				}
			case "B/op":
				if v, err := strconv.ParseInt(val, 10, 64); err == nil {
					res.BPerOp = v
				}
			case "allocs/op":
				if v, err := strconv.ParseInt(val, 10, 64); err == nil {
					res.AllocsOp = v
				}
			}
		}
		if res.NsPerOp > 0 {
			out = append(out, res)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// stripGoroutineSuffix removes the trailing "-N" GOMAXPROCS marker
// so a baseline captured on an 8-core CI matches a current capture
// on a 16-core runner.
func stripGoroutineSuffix(name string) string {
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		// The suffix is "-N" where N is a positive integer.
		if _, err := strconv.Atoi(name[i+1:]); err == nil {
			return name[:i]
		}
	}
	return name
}
