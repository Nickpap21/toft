package envvar

import (
	"errors"
	"fmt"
	"regexp"
)

// keyPattern is a portable environment variable name. Keys are written as
// KEY=VALUE for containers and into a compose .env file, so a key with "=",
// a newline or a quote could inject other variables.
var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ErrInvalidKey is returned for an env var or secret key that is not a
// valid name.
var ErrInvalidKey = errors.New("invalid environment variable name")

// ValidKey reports whether key matches ^[A-Za-z_][A-Za-z0-9_]*$.
func ValidKey(key string) bool {
	return keyPattern.MatchString(key)
}

func validateKey(key string) error {
	if ValidKey(key) {
		return nil
	}
	return fmt.Errorf("%w: %q must start with a letter or underscore and contain only letters, digits and underscores", ErrInvalidKey, key)
}
