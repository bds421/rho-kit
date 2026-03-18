package redisbackend

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewPublisher_NotNil(t *testing.T) {
	p := NewPublisher(nil)
	assert.NotNil(t, p)
}
