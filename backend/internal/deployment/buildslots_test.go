package deployment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSlotsBoundConcurrency(t *testing.T) {
	s := &Service{}
	s.WithBuildSlots(2)
	var logged []string
	log := func(l string) { logged = append(logged, l) }

	r1, ok := s.acquireBuildSlot(context.Background(), nil, log)
	require.True(t, ok)
	r2, ok := s.acquireBuildSlot(context.Background(), nil, log)
	require.True(t, ok)
	assert.Empty(t, logged, "the first two do not wait")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, ok = s.acquireBuildSlot(ctx, nil, log)
	assert.False(t, ok, "the third waits, and gives up when its deploy is cancelled")
	assert.Equal(t, []string{"Waiting for a build slot (2 builds running)..."}, logged)

	r1()
	r3, ok := s.acquireBuildSlot(context.Background(), nil, log)
	require.True(t, ok, "a released slot is taken")
	r2()
	r3()

	unbounded := &Service{}
	_, ok = unbounded.acquireBuildSlot(context.Background(), nil, log)
	assert.True(t, ok)
}

// The wait for a build slot doesn't count toward the deploy's time limit
// (D-125): the clock pauses while it waits, and runs out afterwards.
func TestClockPausesWhileWaitingForABuildSlot(t *testing.T) {
	old := timeoutUnit
	timeoutUnit = 10 * time.Millisecond
	defer func() { timeoutUnit = old }()
	s := &Service{}
	s.WithBuildSlots(1)
	held, ok := s.acquireBuildSlot(context.Background(), nil, func(string) {})
	require.True(t, ok)

	ctx, clock := startClock(context.Background(), 5) // 50 ms
	defer clock.stop()
	go func() {
		time.Sleep(150 * time.Millisecond)
		held()
	}()
	var logged []string
	release, ok := s.acquireBuildSlot(ctx, clock, func(l string) { logged = append(logged, l) })
	require.True(t, ok, "three times the limit spent waiting, and the deploy still runs")
	defer release()
	require.NoError(t, ctx.Err())
	require.Len(t, logged, 2)
	assert.True(t, strings.HasSuffix(logged[1], "the wait doesn't count toward the deploy time limit"), logged[1])
	assert.Greater(t, clock.timeLeft(), 10*time.Millisecond, "the limit starts again where it paused")

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the limit never ran out after the wait")
	}
	var timedOut *TimedOutError
	require.True(t, errors.As(context.Cause(ctx), &timedOut))
	assert.Equal(t, 5, timedOut.Minutes)
}
