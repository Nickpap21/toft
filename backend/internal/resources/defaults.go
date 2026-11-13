package resources

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	slugKinds = map[string]Kind{
		"postgres": KindDatabase, "mysql": KindDatabase, "mongodb": KindDatabase,
		"redis":   KindCache,
		"pgadmin": KindTool, "phpmyadmin": KindTool, "mailhog": KindTool, "mailpit": KindTool, "adminer": KindTool, "redisinsight": KindTool,
	}
	imageKinds = map[string]Kind{
		"postgres": KindDatabase, "postgresql": KindDatabase, "mysql": KindDatabase, "mariadb": KindDatabase, "mongo": KindDatabase,
		"redis": KindCache, "valkey": KindCache,
	}
	// toolImages are admin tools by their image's name (D-150), as their
	// catalog slug, which only a service being created is judged by
	// (NewKindOf).
	toolImages = map[string]string{"pgadmin4": "pgadmin", "phpmyadmin": "phpmyadmin", "mailhog": "mailhog", "mailpit": "mailpit", "adminer": "adminer", "redisinsight": "redisinsight"}
	defaultMB  = map[Kind]int64{KindDatabase: 1024, KindCache: 256, KindApp: 512}
	// toolMB are each admin tool's default: about twice what it used idle
	// after one page load, rounded up to 64 MiB (measured 2026-11-03 with a
	// 1 GiB limit, D-150: Adminer 19.7, phpMyAdmin 32.0, RedisInsight 109.9
	// MiB; pgAdmin's 512 is the founders', it idles near its old 256;
	// Mailpit 7.3 MiB on 2026-11-05).
	// MailHog's image runs on amd64 only and isn't measured yet: it keeps
	// the old tool default.
	toolMB = map[string]int64{"pgadmin": 512, "adminer": 64, "phpmyadmin": 128, "redisinsight": 256, "mailhog": 256, "mailpit": 64}
)

// KindOf is a service's kind: its catalog template's when it has one,
// otherwise its image's name (without registry, path, tag or digest).
func KindOf(addonSlug, image string) Kind {
	if k, ok := slugKinds[addonSlug]; ok {
		return k
	}
	if k, ok := imageKinds[imageName(image)]; ok {
		return k
	}
	return KindApp
}

// NewKindOf is KindOf for a service being created: an admin tool typed in
// by its image counts as a tool too (D-150). Existing services, and every
// compose stack's services, keep KindOf, so their memory suggestions and
// defaults don't change under them.
func NewKindOf(addonSlug, image string) Kind {
	if k := KindOf(addonSlug, image); k != KindApp || toolImages[imageName(image)] == "" {
		return k
	}
	return KindTool
}

func imageName(image string) string {
	name := strings.ToLower(strings.TrimSpace(image))
	if i := strings.Index(name, "@"); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.Index(name, ":"); i >= 0 {
		name = name[:i]
	}
	return name
}

// DefaultMemoryMB is the default memory limit, in MiB, of a service of kind
// k running addonSlug or image: an admin tool's own, otherwise its kind's.
func DefaultMemoryMB(k Kind, addonSlug, image string) int64 {
	if k == KindTool {
		if mb, ok := toolMB[addonSlug]; ok {
			return mb
		}
		if mb, ok := toolMB[toolImages[imageName(image)]]; ok {
			return mb
		}
	}
	if mb, ok := defaultMB[k]; ok {
		return mb
	}
	return defaultMB[KindApp]
}

var plainDecimal = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// maxMB caps ParseMB at 1 PiB so callers can shift the result by 20 bits
// into bytes without overflowing int64.
const maxMB = 1 << 30

// ParseMB reads a memory limit as the service settings store it ("512m",
// "1.5g", "524288k", plain bytes), in MiB rounded up. ok is false for an
// empty, invalid, non-positive or over-1-PiB value. Only plain
// decimals are accepted (no signs, exponents, hex or separators).
func ParseMB(s string) (int64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	mult := 1.0 / (1024 * 1024) // plain bytes
	for _, u := range []struct {
		suffix string
		mib    float64
	}{
		{"gb", 1024}, {"mb", 1}, {"kb", 1.0 / 1024}, {"g", 1024}, {"m", 1}, {"k", 1.0 / 1024},
	} {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSuffix(s, u.suffix), u.mib
			break
		}
	}
	if !plainDecimal.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	mb := math.Ceil(n * mult)
	if mb > maxMB {
		return 0, false
	}
	return int64(mb), true
}

// FormatMB writes MiB as a memory limit string: whole GiB as "1g", else "768m".
func FormatMB(mb int64) string {
	if mb%1024 == 0 {
		return strconv.FormatInt(mb/1024, 10) + "g"
	}
	return strconv.FormatInt(mb, 10) + "m"
}
