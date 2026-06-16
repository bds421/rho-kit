package redis

import (
	"fmt"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// redisNumArgFormat reproduces how Redis serialises a Lua *number* passed as a
// command argument: with %.14g. Current Unix-microsecond timestamps have 16
// significant digits, so a raw Lua number would be rounded to roughly a 100µs
// grid. (miniredis / gopher-lua keep full precision, so a behavioural Allow
// test against miniredis cannot observe this; we model real Redis here.)
func redisNumArgFormat(n float64) string {
	s := fmt.Sprintf("%.14g", n)
	// Redis re-parses the %g output to an integer when it has no fraction;
	// emulate the value that a subsequent tonumber(GET) would read back.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// TestTAT_RawLuaNumberArgWouldLosePrecision documents the root cause: writing a
// current Unix-microsecond TAT as a raw Lua number truncates sub-100µs rate
// increments under Redis's %.14g argument serialisation. This anchors why the
// script must format the value as a decimal string.
func TestTAT_RawLuaNumberArgWouldLosePrecision(t *testing.T) {
	nowUS := float64(time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC).UnixMicro())

	cases := []struct {
		name   string
		rateUS float64
		// preserved is whether storing tat+rate as a RAW Lua number would keep
		// the increment after %.14g serialisation.
		preserved bool
	}{
		{"rate=1us", 1, false},
		{"rate=100us", 100, true},
		{"rate=1ms", 1000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newTat := nowUS + tc.rateUS
			rawStored := redisNumArgFormat(newTat)
			baseStored := redisNumArgFormat(nowUS)
			lost := rawStored == baseStored
			if tc.preserved {
				assert.NotEqual(t, baseStored, rawStored,
					"rate %g should survive raw serialisation", tc.rateUS)
			} else {
				assert.True(t, lost,
					"raw Lua number arg loses the +%gµs increment under %%.14g (got %q)",
					tc.rateUS, rawStored)
			}
		})
	}
}

// setArgRe captures the value expression the GCRA script passes to SET KEYS[1]
// (everything between KEYS[1] and the "EX" TTL option). The value may itself
// contain commas, e.g. string.format("%.0f", newTat).
var setArgRe = regexp.MustCompile(`redis\.call\("SET",\s*KEYS\[1\],\s*(.+?),\s*"EX"`)

// TestGCRAScript_WritesTATAsFormattedString verifies the fix: the script must
// NOT pass newTat to SET as a bare Lua number (which Redis serialises with
// %.14g, see TestTAT_RawLuaNumberArgWouldLosePrecision). It must format it to a
// full-precision decimal string with string.format("%.0f", ...). This fails on
// the pre-fix source `redis.call("SET", KEYS[1], newTat, ...)` and passes once
// the value is wrapped in string.format.
func TestGCRAScript_WritesTATAsFormattedString(t *testing.T) {
	m := setArgRe.FindStringSubmatch(gcraScriptSrc)
	require.Len(t, m, 2, "could not locate the SET KEYS[1] value argument in the script")

	valueArg := m[1]
	assert.NotEqual(t, "newTat", valueArg,
		"TAT must not be SET as a raw Lua number (Redis would truncate it via %%.14g)")
	assert.Contains(t, valueArg, `string.format("%.0f"`,
		"TAT must be SET as a full-precision decimal string")
	assert.Contains(t, valueArg, "newTat",
		"the formatted SET value must be derived from newTat")
}
