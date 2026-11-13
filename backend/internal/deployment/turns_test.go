package deployment

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestTakeTurn(t *testing.T) {
	s := &Service{}
	open := func(c <-chan struct{}) bool {
		select {
		case <-c:
			return false
		default:
			return true
		}
	}

	ahead, first, err := s.takeTurn("web", false)
	require.NoError(t, err)
	assert.Nil(t, ahead, "nothing is under way: no wait")
	other, otherDone, err := s.takeTurn("api", false)
	require.NoError(t, err)
	assert.Nil(t, other, "another service's deploys are a line of their own")
	otherDone()

	second, secondDone, err := s.takeTurn("web", false)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.True(t, open(second), "the second waits for the first")

	// A move waits like any deploy, and nothing is let in behind it: a
	// deploy asked for then was aimed at the server the service leaves.
	move, moveDone, err := s.takeTurn("web", true)
	require.NoError(t, err)
	_, _, err = s.takeTurn("web", false)
	assert.ErrorIs(t, err, ErrServiceMoving)

	first()
	assert.False(t, open(second), "the first ended: the second's turn")
	assert.True(t, open(move), "the move still waits for the second")
	secondDone()
	assert.False(t, open(move))
	_, _, err = s.takeTurn("web", false)
	assert.ErrorIs(t, err, ErrServiceMoving, "refused until the move has ended")
	moveDone()

	ahead, done, err := s.takeTurn("web", false)
	require.NoError(t, err)
	assert.Nil(t, ahead, "the line is empty again")

	// A turn given up while the deploy ahead still runs (a deploy refused
	// after it took its turn) stays in the line until that one has ended.
	_, refused, err := s.takeTurn("web", false)
	require.NoError(t, err)
	refused()
	next, nextDone, err := s.takeTurn("web", false)
	require.NoError(t, err)
	require.NotNil(t, next)
	assert.True(t, open(next), "it waits for the deploy that still runs")
	done()
	require.Eventually(t, func() bool { return !open(next) }, time.Second, time.Millisecond)
	nextDone()
	assert.Empty(t, s.turns)
}

// turnRepo is the part of Repository waitTurn uses: the deployment is there
// or gone, and waiting gets each log line.
type turnRepo struct {
	Repository
	gone    bool
	waiting chan string
}

func (r *turnRepo) InsertLog(_ context.Context, _, msg string) (*Log, error) {
	r.waiting <- msg
	return &Log{Message: msg}, nil
}

func (r *turnRepo) GetByID(context.Context, string) (*Deployment, error) {
	if r.gone {
		return nil, ErrDeploymentNotFound
	}
	return &Deployment{}, nil
}

// A deploy that waited runs, unless its service was deleted meanwhile and
// its deployment with it, or the panel is shutting down: that may be what
// ended the deploy ahead, and one started now would be cut short too.
func TestWaitTurn(t *testing.T) {
	repo := &turnRepo{waiting: make(chan string, 1)}
	s := &Service{repo: repo, log: zap.NewNop()}
	ended := make(chan struct{})
	close(ended)
	assert.True(t, s.waitTurn("d", nil), "no deploy ahead")
	assert.True(t, s.waitTurn("d", ended), "the deploy ahead has ended")
	assert.Empty(t, repo.waiting, "neither had to wait")

	for _, gone := range []bool{false, true} {
		repo.gone = gone
		ahead := make(chan struct{})
		runs := make(chan bool)
		go func() { runs <- s.waitTurn("d", ahead) }()
		assert.Equal(t, "Waiting for this service's earlier deploy to finish...", <-repo.waiting)
		close(ahead)
		assert.Equal(t, !gone, <-runs)
	}

	repo.gone = false
	ahead := make(chan struct{})
	runs := make(chan bool)
	go func() { runs <- s.waitTurn("d", ahead) }()
	<-repo.waiting
	s.BeginShutdown()
	close(ahead)
	assert.False(t, <-runs, "the panel is shutting down: it stays pending")
	assert.False(t, s.waitTurn("d", ended), "also when the deploy ahead had ended before it looked")
}
