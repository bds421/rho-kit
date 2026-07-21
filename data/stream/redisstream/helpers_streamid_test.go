package redisstream

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCompareStreamID_NumericOrder(t *testing.T) {
	// Same millisecond, differing seq digit widths: lexicographic would put
	// "...-10" before "...-2"; Redis order is numeric.
	assert.Equal(t, -1, compareStreamID("1000-2", "1000-10"))
	assert.Equal(t, 1, compareStreamID("1000-10", "1000-2"))
	assert.Equal(t, 0, compareStreamID("1000-2", "1000-2"))
	assert.Equal(t, -1, compareStreamID("999-99", "1000-0"))
}

func TestSortStreamIDs_NumericOrder(t *testing.T) {
	ids := []string{"1000-10", "1000-2", "999-1", "1000-3"}
	sortStreamIDs(ids)
	assert.Equal(t, []string{"999-1", "1000-2", "1000-3", "1000-10"}, ids)
}
