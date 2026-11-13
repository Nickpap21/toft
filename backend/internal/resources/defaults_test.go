package resources

import "testing"

func TestKindOf(t *testing.T) {
	cases := []struct {
		slug, image string
		want        Kind
	}{
		{"postgres", "", KindDatabase},
		{"mongodb", "", KindDatabase},
		{"redis", "", KindCache},
		{"adminer", "", KindTool},
		{"redisinsight", "", KindTool},
		{"", "postgres:16", KindDatabase},
		{"", "docker.io/library/mariadb:11", KindDatabase},
		{"", "bitnami/postgresql:15", KindDatabase},
		{"", "ghcr.io/acme/mongo@sha256:abc", KindDatabase},
		{"", "valkey/valkey:8-alpine", KindCache},
		{"", "localhost:5000/redis", KindCache},
		{"", "dpage/pgadmin4:8", KindApp}, // a tool only when new: NewKindOf
		{"", "nginx:1.27", KindApp},
		{"", "", KindApp},
		{"minio", "minio/minio", KindApp},
	}
	for _, c := range cases {
		if got := KindOf(c.slug, c.image); got != c.want {
			t.Errorf("KindOf(%q, %q) = %q, want %q", c.slug, c.image, got, c.want)
		}
	}
}

// An admin tool typed in by its image is a tool only for a service being
// created (D-150); KindOf, which existing services and compose stacks
// use, still calls it an app.
func TestNewKindOf(t *testing.T) {
	for _, c := range []struct {
		slug, image string
		want        Kind
	}{
		{"", "dpage/pgadmin4:8", KindTool},
		{"", "redis/redisinsight:latest", KindTool},
		{"", "phpmyadmin", KindTool},
		{"", "postgres:16", KindDatabase},
		{"adminer", "", KindTool},
		{"", "nginx:1.27", KindApp},
	} {
		if got := NewKindOf(c.slug, c.image); got != c.want {
			t.Errorf("NewKindOf(%q, %q) = %q, want %q", c.slug, c.image, got, c.want)
		}
	}
	if got := KindOf("", "redis/redisinsight:latest"); got != KindApp {
		t.Errorf("KindOf(redisinsight image) = %q, want app", got)
	}
}

func TestDefaultMemoryMB(t *testing.T) {
	for _, c := range []struct {
		slug, image string
		want        int64
	}{
		{"postgres", "postgres:16", 1024},
		{"redis", "redis:7-alpine", 256},
		{"", "nginx:1.27", 512},
		{"pgadmin", "dpage/pgadmin4", 512},
		{"adminer", "adminer", 64},
		{"phpmyadmin", "phpmyadmin", 128},
		{"redisinsight", "redis/redisinsight:latest", 256},
		{"mailhog", "mailhog/mailhog", 256},
		{"mailpit", "axllent/mailpit:v1.33.0@sha256:abc", 64},
		{"", "axllent/mailpit", 64},
		{"", "dpage/pgadmin4:9", 512}, // typed in: a tool when created (NewKindOf)
		{"", "docker.io/library/adminer:5@sha256:abc", 64},
	} {
		if got := DefaultMemoryMB(NewKindOf(c.slug, c.image), c.slug, c.image); got != c.want {
			t.Errorf("DefaultMemoryMB(%q, %q) = %d, want %d", c.slug, c.image, got, c.want)
		}
	}
	// An existing typed-in tool is an app (KindOf), and keeps an app's default.
	if got := DefaultMemoryMB(KindOf("", "dpage/pgadmin4:9"), "", "dpage/pgadmin4:9"); got != 512 {
		t.Errorf("an existing typed-in pgAdmin = %d, want an app's 512", got)
	}
	// Every tool in the catalog has its own default.
	for slug, k := range slugKinds {
		if _, ok := toolMB[slug]; k == KindTool && !ok {
			t.Errorf("tool %q has no default in toolMB", slug)
		}
	}
	for image, slug := range toolImages {
		if slugKinds[slug] != KindTool {
			t.Errorf("tool image %q names %q, not a tool's slug", image, slug)
		}
	}
}

func TestParseMB(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"512m", 512, true}, {"512M", 512, true}, {" 512m ", 512, true},
		{"1g", 1024, true}, {"1.5g", 1536, true}, {"1G", 1024, true},
		{"512mb", 512, true}, {"1gb", 1024, true}, {"524288k", 512, true},
		{"536870912", 512, true}, {"1", 1, true}, // bytes round up to 1 MiB
		{"", 0, false}, {"abc", 0, false}, {"-1g", 0, false}, {"0m", 0, false},
		{"nan", 0, false}, {"nanm", 0, false}, {"1e3m", 0, false}, {"0x10m", 0, false},
		{"+5m", 0, false}, {"1.m", 0, false}, {"99999999999999999999g", 0, false},
		{"1048576g", 1073741824, true}, {"1048577g", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseMB(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseMB(%q) = %d, %v, want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFormatMB(t *testing.T) {
	for mb, want := range map[int64]string{1024: "1g", 2048: "2g", 768: "768m", 64: "64m", 1536: "1536m"} {
		if got := FormatMB(mb); got != want {
			t.Errorf("FormatMB(%d) = %q, want %q", mb, got, want)
		}
	}
}
