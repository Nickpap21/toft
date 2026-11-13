package preview

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestIsMigration(t *testing.T) {
	for p, want := range map[string]bool{
		"migrations/0001_init.sql":                          true,
		"backend/db/migrations/0061_preview.sql":            true,
		"cmd/migrate/main.go":                               true,
		"db/migrate/20240101000000_create_users.rb":         true,
		"database/migrations/2024_01_01_create_users.php":   true,
		"src/main/resources/db/migration/V1__init.sql":      true,
		"src/main/resources/db/changelog/db.changelog.yaml": true,
		"prisma/migrations/20240101_init/migration.sql":     true,
		"alembic/versions/abc123_init.py":                   true,
		"shop/migrations/0002_order.py":                     true,
		"backend/db/schema.sql":                             true,
		"schema.sql":                                        true,
		"prisma/schema.prisma":                              true,
		"atlas.hcl":                                         true,
		"infra/main.hcl":                                    true,
		"cmd/migrator.go":                                   false,
		"internal/migrate.go":                               false,
		"docs/migrations.md":                                false,
		"migrations":                                        false,
		"premigrations/0001.sql":                            false,
		"Migrations/0001.sql":                               false,
		"db/schema.sql.bak":                                 false,
		"src/schema.ts":                                     false,
		"alembic/env.py":                                    false,
		"config.hcl.example":                                false,
	} {
		if got := IsMigration(p); got != want {
			t.Errorf("IsMigration(%q) = %v, want %v", p, got, want)
		}
	}
}

// A deleted migration counts; a rename names the side that is one.
func TestMigrationFiles(t *testing.T) {
	got := MigrationFiles([]ChangedFile{
		{Path: "main.go"},
		{Path: "db/migrations/0002_drop.sql"},
		{Path: "old/0001.sql", OldPath: "db/migrations/0001.sql"},
		{Path: "db/migrations/0003.sql", OldPath: "tmp/0003.sql"},
	})
	if want := []string{"db/migrations/0002_drop.sql", "db/migrations/0001.sql", "db/migrations/0003.sql"}; !slices.Equal(got, want) {
		t.Fatalf("MigrationFiles = %v, want %v", got, want)
	}
	many := make([]ChangedFile, 0, 60)
	for i := range 60 {
		many = append(many, ChangedFile{Path: fmt.Sprintf("migrations/%04d.sql", i)})
	}
	if got := MigrationFiles(many); len(got) != MaxMigrationFiles || got[0] != "migrations/0000.sql" {
		t.Fatalf("capped = %d, first %q", len(got), got[0])
	}
	if got := MigrationFiles(nil); got == nil || len(got) != 0 {
		t.Fatalf("none = %#v, want empty", got)
	}
}

func TestNames(t *testing.T) {
	if got := EnvironmentName(42, "storefront", true); got != "pr-42" {
		t.Fatalf("EnvironmentName = %q", got)
	}
	if got := EnvironmentName(42, "Store Front", false); got != "store-front-pr-42" {
		t.Fatalf("EnvironmentName other repo = %q", got)
	}
	if got := Hostname("preview.example.com", "web", 42, "storefront", true); got != "pr-42-web.preview.example.com" {
		t.Fatalf("Hostname = %q", got)
	}
	if got := Hostname("preview.example.com", "web", 42, "mobile", false); got != "mobile-pr-42-web.preview.example.com" {
		t.Fatalf("Hostname other repo = %q", got)
	}
	if got := SslipHostname("203.0.113.10", "web", "storefront", 42, "", true); got != "web-pr42-storefront.203-0-113-10.sslip.io" {
		t.Fatalf("SslipHostname = %q", got)
	}
	long := SslipHostname("203.0.113.10", strings.Repeat("service", 8), strings.Repeat("project", 8), 7, "", true)
	other := SslipHostname("203.0.113.10", strings.Repeat("service", 8), strings.Repeat("project", 9), 7, "", true)
	first, _, _ := strings.Cut(long, ".")
	if len(first) > 63 || strings.HasSuffix(first, "-") || long == other {
		t.Fatalf("long label %q (%d), other %q", first, len(first), other)
	}
	if label("--Web__API--") != "web-api" {
		t.Fatalf("label = %q", label("--Web__API--"))
	}
	if d := RegisteredDomain("pr-42-web.preview.example.co.uk"); d != "example.co.uk" {
		t.Fatalf("RegisteredDomain = %q", d)
	}
}

func TestRunFlags(t *testing.T) {
	df := strings.Join([]string{
		"# syntax=docker/dockerfile:1",
		"FROM node:20",
		"# RUN --mount=type=cache,target=/x is a comment",
		"RUN npm ci",
		`run --mount=type=cache,target=/root/.npm \`,
		"    --network=host npm ci",
		"RUN <<DONE",
		"--mount=not-a-flag",
		"RUN --security=insecure inside a heredoc",
		"DONE",
		"RUN echo done",
		"RUN --security=insecure make",
	}, "\n")
	got := RunFlags([]byte(df))
	want := []string{"--mount", "--network", "--security"}
	if !slices.Equal(got, want) {
		t.Fatalf("RunFlags = %v, want %v", got, want)
	}
	if f := RunFlags([]byte("FROM nginx\nRUN echo --mount\nCOPY . /app\n")); len(f) != 0 {
		t.Fatalf("plain Dockerfile flagged: %v", f)
	}
}

func TestVariables(t *testing.T) {
	base := []string{
		"API_URL=https://api.staging.example.com/v1",
		"CORS=https://web.staging.example.com,https://web.staging.example.com",
		"OTHER=https://admin-api.staging.example.com",
		"DB=postgres://postgres:5432/app",
		"TOFT_PULL_REQUEST=1",
	}
	got := Variables(base, map[string]string{
		"api.staging.example.com": "pr-42-api.preview.example.com",
		"web.staging.example.com": "pr-42-web.preview.example.com",
	}, map[string]string{"TOFT_PULL_REQUEST": "42", "TOFT_PREVIEW_URL": "https://pr-42-web.preview.example.com"})
	want := []string{
		"API_URL=https://pr-42-api.preview.example.com/v1",
		"CORS=https://pr-42-web.preview.example.com,https://pr-42-web.preview.example.com",
		"OTHER=https://admin-api.staging.example.com",
		"DB=postgres://postgres:5432/app",
		"TOFT_PREVIEW_URL=https://pr-42-web.preview.example.com",
		"TOFT_PULL_REQUEST=42",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Variables =\n%v\nwant\n%v", got, want)
	}
}

func TestPreviewStatus(t *testing.T) {
	active := &Preview{State: StateActive}
	for _, c := range []struct {
		services []CopyService
		want     string
	}{
		{[]CopyService{{RuntimeStatus: "stopped", DeploymentStatus: "failed"}}, "failed"},
		{[]CopyService{{DeploymentStatus: "building"}, {DeploymentStatus: "pending"}}, "starting"},
		{[]CopyService{{RuntimeStatus: "running", DeploymentStatus: "running", DeploymentSHA: "a"}, {DeploymentStatus: "building", DeploymentSHA: "b"}}, "updating"},
		{[]CopyService{{RuntimeStatus: "running", DeploymentStatus: "running"}}, "running"},
		{[]CopyService{{RuntimeStatus: "stopped", DeploymentStatus: "running"}}, "stopped"},
		{[]CopyService{{RuntimeStatus: "stopped", DeploymentStatus: "pending"}}, "starting"},
	} {
		views := make([]ServiceView, 0, len(c.services))
		for _, s := range c.services {
			views = append(views, ServiceView{Status: serviceStatus(s), SHA: s.DeploymentSHA})
		}
		if got := previewStatus(active, views); got != c.want {
			t.Errorf("%+v: %s, want %s", c.services, got, c.want)
		}
	}
	if got := previewStatus(&Preview{State: StateWaitingApproval, Note: "no gVisor"}, nil); got != "blocked" {
		t.Errorf("blocked: %s", got)
	}
}
