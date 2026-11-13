package apikey

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLastUsedWrittenOnceAMinutePerKey(t *testing.T) {
	s := &Service{}
	t0 := time.Now()
	assert.True(t, s.shouldWriteLastUsed("k1", t0))
	assert.False(t, s.shouldWriteLastUsed("k1", t0.Add(30*time.Second)))
	assert.True(t, s.shouldWriteLastUsed("k2", t0.Add(30*time.Second)), "another key has its own clock")
	assert.True(t, s.shouldWriteLastUsed("k1", t0.Add(61*time.Second)))
}
