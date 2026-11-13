package composespec

import "regexp"

// serviceNamePattern is what the platform accepts as the compose service
// that receives a service's domains (Settings → Domain Routing): letters,
// digits, '_', '.' and '-', starting with a letter or digit, at most 63
// characters. Compose accepts these as service names, and none of them can
// carry YAML or interpolation syntax ('$', ':', '{', quotes, spaces).
var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// ValidServiceName reports whether name is a compose service name the
// platform can route domains to.
func ValidServiceName(name string) bool {
	return serviceNamePattern.MatchString(name)
}
