package instance_test

import (
	"context"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/internal/testdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Audit log's "The panel" and "Accounts" filter in the query, so a page
// holds a full page of its kind and a cursor continues inside that kind.
func TestListAuditFiltersByKindAcrossPages(t *testing.T) {
	repo := instance.NewPostgresRepository(testdb.New(t))
	ctx := context.Background()

	// Oldest first, the kinds interleaved. "user.setup.renamed" names a
	// panel word but doesn't start with one: it is an account's.
	for _, action := range []string{
		"agents.paused",
		"agents.resumed",
		"panel.domain_changed",
		"user.deactivated",
		"recovery_kit.viewed",
		"operator.granted",
		"setup.finished",
		"user.setup.renamed",
		"panel.shell_opened",
	} {
		require.NoError(t, repo.WriteAudit(ctx, "", action, "", nil))
	}

	// walk reads every page of a kind, two rows at a time.
	walk := func(kind string) [][]string {
		var pages [][]string
		var before *time.Time
		beforeID := ""
		for {
			rows, err := repo.ListAudit(ctx, 2, before, beforeID, kind)
			require.NoError(t, err)
			if len(rows) == 0 {
				return pages
			}
			page := make([]string, 0, len(rows))
			for _, e := range rows {
				page = append(page, e.Action)
			}
			pages = append(pages, page)
			last := rows[len(rows)-1]
			before, beforeID = &last.CreatedAt, last.ID
		}
	}

	assert.Equal(t, [][]string{
		{"panel.shell_opened", "setup.finished"},
		{"recovery_kit.viewed", "panel.domain_changed"},
		{"agents.resumed", "agents.paused"},
	}, walk("panel"))
	assert.Equal(t, [][]string{
		{"user.setup.renamed", "operator.granted"},
		{"user.deactivated"},
	}, walk("accounts"))
	assert.Equal(t, [][]string{
		{"panel.shell_opened", "user.setup.renamed"},
		{"setup.finished", "operator.granted"},
		{"recovery_kit.viewed", "user.deactivated"},
		{"panel.domain_changed", "agents.resumed"},
		{"agents.paused"},
	}, walk(""), "no kind, every row")
}
