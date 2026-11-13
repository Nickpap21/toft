package deploypolicy

import "time"

// SetNow replaces the clock, for grace-period tests.
func (s *Service) SetNow(f func() time.Time) { s.now = f }

// VersionOf is what a grant must send for an exception as stored.
var VersionOf = versionOf
