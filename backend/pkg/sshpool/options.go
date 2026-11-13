package sshpool

import (
	"context"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshclient"
	"go.uber.org/zap"
)

// Default tuning. They are code defaults on purpose (no environment
// variables); tests shorten them through Options.
const (
	DefaultMaxSessionsPerConn = 8 // below OpenSSH's default MaxSessions of 10
	DefaultMaxConnsPerServer  = 3
	DefaultKeepaliveInterval  = 30 * time.Second
	DefaultKeepaliveTimeout   = 15 * time.Second
	DefaultDialTimeout        = 10 * time.Second
	DefaultBackoffMin         = time.Second
	DefaultBackoffMax         = time.Minute
	DefaultOverflowIdle       = 2 * time.Minute
	DefaultPrimaryIdle        = 30 * time.Minute
	DefaultReapInterval       = 30 * time.Second
	DefaultFirstUseTimeout    = 10 * time.Second
)

// Dialer opens one SSH connection. The default is sshclient.Dial.
type Dialer func(ctx context.Context, cfg sshclient.Config) (*sshclient.Client, error)

// Options tunes a Pool. Zero fields take the defaults above.
type Options struct {
	// MaxSessionsPerConn is how many sessions (exec, stream, upload, PTY)
	// one connection carries at once. A server that refuses sessions sooner
	// lowers it for that connection.
	MaxSessionsPerConn int
	// MaxConnsPerServer caps the primary plus overflow connections to one
	// server. With every slot taken, callers wait.
	MaxConnsPerServer int
	// KeepaliveInterval is how often each connection is probed, and
	// KeepaliveTimeout how long a probe may go unanswered before the
	// connection is closed.
	KeepaliveInterval time.Duration
	KeepaliveTimeout  time.Duration
	// DialTimeout bounds the TCP connect plus SSH handshake of one dial.
	DialTimeout time.Duration
	// FirstUseTimeout bounds Target.LoadPin and Target.OnFirstUse.
	FirstUseTimeout time.Duration
	// BackoffMin and BackoffMax bound the wait after failed dials: the n-th
	// consecutive failure waits a random time between BackoffMin and
	// min(BackoffMax, BackoffMin·2^(n-1)).
	BackoffMin time.Duration
	BackoffMax time.Duration
	// OverflowIdle and PrimaryIdle are how long an unused overflow or
	// primary connection stays open; the reaper checks every ReapInterval.
	OverflowIdle time.Duration
	PrimaryIdle  time.Duration
	ReapInterval time.Duration
	// Logger receives connection lifecycle events. Nil discards them.
	Logger *zap.Logger
	// Dialer replaces sshclient.Dial (tests count or fail dials with it).
	Dialer Dialer
}

func (o Options) withDefaults() Options {
	setInt := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	setDur := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	setInt(&o.MaxSessionsPerConn, DefaultMaxSessionsPerConn)
	setInt(&o.MaxConnsPerServer, DefaultMaxConnsPerServer)
	setDur(&o.KeepaliveInterval, DefaultKeepaliveInterval)
	setDur(&o.KeepaliveTimeout, DefaultKeepaliveTimeout)
	setDur(&o.DialTimeout, DefaultDialTimeout)
	setDur(&o.FirstUseTimeout, DefaultFirstUseTimeout)
	setDur(&o.BackoffMin, DefaultBackoffMin)
	setDur(&o.BackoffMax, DefaultBackoffMax)
	if o.BackoffMax < o.BackoffMin {
		o.BackoffMax = o.BackoffMin
	}
	setDur(&o.OverflowIdle, DefaultOverflowIdle)
	setDur(&o.PrimaryIdle, DefaultPrimaryIdle)
	setDur(&o.ReapInterval, DefaultReapInterval)
	if o.Logger == nil {
		o.Logger = zap.NewNop()
	}
	if o.Dialer == nil {
		o.Dialer = sshclient.Dial
	}
	return o
}
