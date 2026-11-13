package version

import "errors"

// ErrNotFound: the service is not in the caller's workspace, or does not
// exist; the two are not told apart.
var ErrNotFound = errors.New("service not found")
