// Package reseal seals every stored secret again as v1 under the current
// ENCRYPTION_KEY (D-126): values in the format before v0.28.0, and v1 values
// under ENCRYPTION_KEY_PREVIOUS while a rotation runs. The backend runs it
// at every start, before the API listens; `server reseal --check` only
// counts what is left.
package reseal

import (
	"context"
	"fmt"

	"github.com/Nickpap21/toft/backend/db/sqlcdb"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
)

// row and swap are the shapes every Reseal query shares (db/queries/reseal.sql).
type row = struct {
	ID     string `json:"id"`
	Ident  string `json:"ident"`
	Sealed string `json:"sealed"`
}

type swap = struct {
	Ids       []string `json:"ids"`
	OldValues []string `json:"old_values"`
	NewValues []string `json:"new_values"`
}

type listParams = struct {
	Current string `json:"current"`
	After   string `json:"after"`
}

// place is one sealed column. column is its table and column as crypto.AD
// names it; each module seals with the same name and the row identity the
// query returns as ident (reseal_test.go reads every one back through its
// module).
type place struct {
	column  string
	list    func(ctx context.Context, current, after string) ([]row, error)
	replace func(ctx context.Context, s swap) (int64, error)
}

// listOf and replaceOf adapt a place's sqlc queries, whose generated types
// all have the shared shapes.
func listOf[P ~listParams, R ~row](f func(context.Context, P) ([]R, error)) func(context.Context, string, string) ([]row, error) {
	return func(ctx context.Context, current, after string) ([]row, error) {
		rs, err := f(ctx, P{Current: current, After: after})
		out := make([]row, len(rs))
		for i, r := range rs {
			out[i] = row(r)
		}
		return out, err
	}
}

func replaceOf[P ~swap](f func(context.Context, P) (int64, error)) func(context.Context, swap) (int64, error) {
	return func(ctx context.Context, s swap) (int64, error) { return f(ctx, P(s)) }
}

func places(q *sqlcdb.Queries) []place {
	return []place{
		{"env_vars.value", listOf(q.ResealListEnvVars), replaceOf(q.ResealReplaceEnvVars)},
		{"environment_env_vars.value", listOf(q.ResealListEnvironmentEnvVars), replaceOf(q.ResealReplaceEnvironmentEnvVars)},
		{"services.registry_password", listOf(q.ResealListServiceRegistryPasswords), replaceOf(q.ResealReplaceServiceRegistryPasswords)},
		{"services.git_token", listOf(q.ResealListServiceGitTokens), replaceOf(q.ResealReplaceServiceGitTokens)},
		{"services.basic_auth_password", listOf(q.ResealListServiceBasicAuthPasswords), replaceOf(q.ResealReplaceServiceBasicAuthPasswords)},
		{"servers.ssh_key", listOf(q.ResealListServerSSHKeys), replaceOf(q.ResealReplaceServerSSHKeys)},
		{"server_dns_providers.credentials", listOf(q.ResealListServerDNSCredentials), replaceOf(q.ResealReplaceServerDNSCredentials)},
		{"backup_destinations.secret_key", listOf(q.ResealListBackupSecretKeys), replaceOf(q.ResealReplaceBackupSecretKeys)},
		{"backup_destinations.repo_password", listOf(q.ResealListBackupRepoPasswords), replaceOf(q.ResealReplaceBackupRepoPasswords)},
		{"org_invites.token_encrypted", listOf(q.ResealListInviteTokens), replaceOf(q.ResealReplaceInviteTokens)},
		{"project_webhooks.secret", listOf(q.ResealListProjectWebhookSecrets), replaceOf(q.ResealReplaceProjectWebhookSecrets)},
		{"repository_mirrors.password_encrypted", listOf(q.ResealListMirrorPasswords), replaceOf(q.ResealReplaceMirrorPasswords)},
		{"repository_webhooks.secret_encrypted", listOf(q.ResealListRepositoryWebhookSecrets), replaceOf(q.ResealReplaceRepositoryWebhookSecrets)},
		{"alert_channels.signing_secret", listOf(q.ResealListAlertSigningSecrets), replaceOf(q.ResealReplaceAlertSigningSecrets)},
		{"agent_approvals.sealed", listOf(q.ResealListAgentApprovalSealed), replaceOf(q.ResealReplaceAgentApprovalSealed)},
		{"secret_reveals.sealed", listOf(q.ResealListSecretReveals), replaceOf(q.ResealReplaceSecretReveals)},
		{"instance_settings.value", listOf(q.ResealListInstanceKeys), replaceOf(q.ResealReplaceInstanceKeys)},
	}
}

// Row names a value not under the current key: its column and row key.
type Row struct {
	Column, ID string
	Err        error
}

// Result is a pass: the values sealed again; in a check, those a key opens
// that aren't under the current one (Left); and those no key opens (Lost),
// which dropping ENCRYPTION_KEY_PREVIOUS can't make worse.
type Result struct {
	Resealed int64
	Left     []Row
	Lost     []Row
}

// noID is the first cursor: every UUID, and every instance setting's key
// (compared in the C collation), sorts after it.
const noID = "00000000-0000-0000-0000-000000000000"

// Run seals again every value not under the current key, or, with check,
// only lists them. A value a user changes meanwhile is kept.
func Run(ctx context.Context, q *sqlcdb.Queries, keys *crypto.Keys, check bool) (Result, error) {
	var res Result
	current := "v1." + keys.CurrentID() + ".%"
	for _, p := range places(q) {
		for after := noID; ; {
			rows, err := p.list(ctx, current, after)
			if err != nil {
				return res, fmt.Errorf("list %s: %w", p.column, err)
			}
			if len(rows) == 0 {
				break
			}
			after = rows[len(rows)-1].ID
			s := swap{Ids: make([]string, 0, len(rows)), OldValues: make([]string, 0, len(rows)), NewValues: make([]string, 0, len(rows))}
			for _, r := range rows {
				ad := crypto.AD(p.column, r.Ident)
				plain, err := keys.OpenLegacy(ad, r.Sealed)
				if err != nil {
					res.Lost = append(res.Lost, Row{Column: p.column, ID: r.ID, Err: err})
					continue
				}
				if check {
					res.Left = append(res.Left, Row{Column: p.column, ID: r.ID})
					continue
				}
				sealed, err := keys.Seal(ad, plain)
				if err != nil {
					return res, fmt.Errorf("seal %s %s: %w", p.column, r.ID, err)
				}
				s.Ids, s.OldValues, s.NewValues = append(s.Ids, r.ID), append(s.OldValues, r.Sealed), append(s.NewValues, sealed)
			}
			if len(s.Ids) == 0 {
				continue
			}
			n, err := p.replace(ctx, s)
			if err != nil {
				return res, fmt.Errorf("replace %s: %w", p.column, err)
			}
			res.Resealed += n
		}
	}
	return res, nil
}
