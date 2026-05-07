package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_StandardOutput(t *testing.T) {
	input := `goos: darwin
goarch: arm64
pkg: example.com/x
BenchmarkFoo-8         	 1000000	      1234 ns/op	     456 B/op	       7 allocs/op
BenchmarkBar-8         	  500000	      2468 ns/op	     128 B/op	       2 allocs/op
PASS
ok  	example.com/x	2.345s
`
	res, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	require.Len(t, res, 2)

	assert.Equal(t, "BenchmarkFoo", res[0].Name, "GOMAXPROCS suffix should be stripped")
	assert.Equal(t, 1234.0, res[0].NsPerOp)
	assert.Equal(t, int64(456), res[0].BPerOp)
	assert.Equal(t, int64(7), res[0].AllocsOp)
	assert.Equal(t, int64(1000000), res[0].Iterations)
}

func TestParse_NoBenchmemFields(t *testing.T) {
	input := `BenchmarkX-4    100  10000 ns/op
`
	res, err := Parse(strings.NewReader(input))
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, 10000.0, res[0].NsPerOp)
	assert.Zero(t, res[0].BPerOp)
}

func TestStripGoroutineSuffix(t *testing.T) {
	tests := map[string]string{
		"BenchmarkFoo-8":          "BenchmarkFoo",
		"BenchmarkBar-128":        "BenchmarkBar",
		"BenchmarkNoSuffix":       "BenchmarkNoSuffix",
		"BenchmarkSubtest/case-8": "BenchmarkSubtest/case",
	}
	for in, want := range tests {
		assert.Equal(t, want, stripGoroutineSuffix(in), "input=%q", in)
	}
}
