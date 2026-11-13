package configfile

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// ErrInvalidPath is returned for a config file path that could be written
// outside the service's config directory, or that Docker cannot mount.
var ErrInvalidPath = errors.New("invalid config file path")

// ValidatePath checks a config file path. The path is the file's location
// inside the container, which Docker requires to be absolute (for example
// /etc/nginx/conf.d/default.conf). It is also the file's location below
// /var/paas/config/<serviceID>/ on the host, so a ".." segment, which would
// write outside that directory, is refused, as are relative paths, the root
// itself and control characters.
func ValidatePath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: the path is empty", ErrInvalidPath)
	}
	if strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return fmt.Errorf("%w: the path contains control characters", ErrInvalidPath)
	}
	if strings.Contains(p, `\`) {
		return fmt.Errorf("%w: use forward slashes", ErrInvalidPath)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return fmt.Errorf("%w: %q contains \"..\"", ErrInvalidPath, p)
		}
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: %q must be an absolute path inside the container, such as /etc/app/config.yml", ErrInvalidPath, p)
	}
	if path.Clean(p) == "/" {
		return fmt.Errorf("%w: the path must name a file", ErrInvalidPath)
	}
	return nil
}

// hostPathFor returns where a config file is written on the host, and an
// error when the path is invalid or would land outside the service's
// directory (defence in depth for rows stored before ValidatePath existed).
func hostPathFor(base, serviceID, p string) (string, error) {
	if err := ValidatePath(p); err != nil {
		return "", err
	}
	dir := filepath.Join(base, serviceID)
	hostPath := filepath.Join(dir, p)
	rel, err := filepath.Rel(dir, hostPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%w: %q leaves the service's config directory", ErrInvalidPath, p)
	}
	return hostPath, nil
}
