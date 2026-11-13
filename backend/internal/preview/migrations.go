package preview

import (
	"path"
	"strings"
)

// MaxMigrationFiles caps the migrations a preview keeps and names.
const MaxMigrationFiles = 50

// ChangedFile is a file a pull request's diff touches: OldPath is set for
// a rename or a copy.
type ChangedFile struct {
	Path, OldPath string
}

// migrationDirs are where migration tools keep them: migrations/ (Laravel's
// database/, Prisma's, Django's <app>/, golang-migrate, Atlas), migrate/
// (Rails' db/), Flyway's db/migration/, Liquibase's db/changelog/ and
// Alembic's versions/.
var migrationDirs = []string{"/migrations/", "/migrate/", "/db/migration/", "/db/changelog/", "/alembic/versions/"}

// IsMigration reports whether a repository path (relative, with /) is a
// migration or a schema a migration tool applies (D-144).
func IsMigration(p string) bool {
	if b := path.Base(p); b == "schema.sql" || b == "schema.prisma" {
		return true
	}
	// ponytail: every .hcl counts (Atlas), Terraform's, Packer's and Nomad's
	// too; it's a warning only, so a schema-file check if they get noisy.
	if strings.HasSuffix(p, ".hcl") {
		return true
	}
	for _, d := range migrationDirs {
		if strings.Contains("/"+p, d) {
			return true
		}
	}
	return false
}

// MigrationFiles are the migrations among a diff's files, the first
// MaxMigrationFiles: a file's path, or the one it was renamed from when
// only that is one. A deleted migration counts.
func MigrationFiles(files []ChangedFile) []string {
	out := make([]string, 0)
	for _, f := range files {
		if len(out) == MaxMigrationFiles {
			break
		}
		switch {
		case IsMigration(f.Path):
			out = append(out, f.Path)
		case f.OldPath != "" && IsMigration(f.OldPath):
			out = append(out, f.OldPath)
		}
	}
	return out
}
