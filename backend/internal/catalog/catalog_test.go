package catalog_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/catalog"
)

// Mailpit replaced MailHog: the catalog offers Mailpit only, and a service
// made from MailHog still finds its template (its connect-to port).
func TestMailpitReplacesMailHog(t *testing.T) {
	slugs := make([]string, 0, len(catalog.All()))
	for _, tmpl := range catalog.All() {
		slugs = append(slugs, tmpl.Slug)
	}
	if !slices.Contains(slugs, "mailpit") || slices.Contains(slugs, "mailhog") {
		t.Fatalf("the catalog offers %v, want mailpit and not mailhog", slugs)
	}
	for _, slug := range []string{"mailpit", "mailhog"} {
		tmpl, ok := catalog.Get(slug)
		if !ok || tmpl.Kind != catalog.KindImage || tmpl.DefaultPort != 8025 || tmpl.Category != "tools" {
			t.Errorf("Get(%q) = %+v, %v; want a tools image on 8025", slug, tmpl, ok)
		}
	}
}

// Every catalog entry says how its admin login is kept from the defaults
// the internet knows (admin/admin, minioadmin, guest/guest, admin/umami) or
// from whoever opens it first. A new entry fails here until it is
// classified, and a class whose condition doesn't hold fails too.
func TestEveryEntryHasNoDefaultLogin(t *testing.T) {
	const (
		password    = "password"    // the image takes its admin password from this secret spec, generated at creation
		setByToft   = "set by Toft" // the image has a fixed default and no variable: a compose service sets this secret spec on it
		setupLogin  = "setup login" // whoever opens it first becomes its admin: a generated basic auth until then (SetupLogin)
		ownLogin    = "own login"   // signs in with the login of the database it opens
		noLoginHere = "no login"    // no login of its own, and internal until someone adds a domain
	)
	classes := map[string][2]string{
		"postgres":     {password, "POSTGRES_PASSWORD"},
		"mysql":        {password, "MYSQL_ROOT_PASSWORD"},
		"mongodb":      {password, "MONGO_INITDB_ROOT_PASSWORD"},
		"pgadmin":      {password, "PGADMIN_DEFAULT_PASSWORD"},
		"minio":        {password, "MINIO_ROOT_PASSWORD"},
		"typesense":    {password, "TYPESENSE_API_KEY"},
		"rabbitmq":     {password, "RABBITMQ_DEFAULT_PASS"},
		"grafana":      {password, "GF_SECURITY_ADMIN_PASSWORD"},
		"umami":        {setByToft, "UMAMI_ADMIN_PASSWORD"},
		"uptime-kuma":  {setupLogin},
		"n8n":          {setupLogin}, // its owner variables lock the account (2.46.4)
		"ghost":        {setupLogin},
		"plausible":    {setupLogin},
		"adminer":      {ownLogin},
		"phpmyadmin":   {ownLogin},
		"redis":        {noLoginHere},
		"mailpit":      {noLoginHere},
		"prometheus":   {noLoginHere},
		"loki":         {noLoginHere},
		"redisinsight": {noLoginHere},
		"nginx":        {noLoginHere},
	}
	secret := func(tmpl catalog.Template, key string) bool {
		i := slices.IndexFunc(tmpl.EnvVarSpecs, func(s catalog.EnvVarSpec) bool { return s.Key == key })
		return i >= 0 && tmpl.EnvVarSpecs[i].IsSecret
	}
	for _, tmpl := range catalog.All() {
		c, ok := classes[tmpl.Slug]
		if !ok {
			t.Errorf("%s: say how its admin login is kept from a default (classes above)", tmpl.Slug)
			continue
		}
		delete(classes, tmpl.Slug)
		if tmpl.SetupLogin != (c[0] == setupLogin) {
			t.Errorf("%s: SetupLogin is %v, class %q", tmpl.Slug, tmpl.SetupLogin, c[0])
		}
		if want := map[bool]string{true: c[1]}[c[0] == password || c[0] == setByToft]; tmpl.AdminPassword != want {
			t.Errorf("%s: AdminPassword is %q, want %q (kept for its creator, D-160)", tmpl.Slug, tmpl.AdminPassword, want)
		}
		switch c[0] {
		case password:
			if !secret(tmpl, c[1]) {
				t.Errorf("%s: %s is not a secret spec, so nothing generates it", tmpl.Slug, c[1])
			}
		case setByToft:
			if !secret(tmpl, c[1]) || !strings.Contains(tmpl.ComposeContent, "${"+c[1]+"}") {
				t.Errorf("%s: %s must be a secret spec its compose file reads", tmpl.Slug, c[1])
			}
		case setupLogin:
			if tmpl.Category != "apps" {
				t.Errorf("%s: a setup login is for an app with a temporary address", tmpl.Slug)
			}
		case ownLogin, noLoginHere:
			if tmpl.Category == "apps" {
				t.Errorf("%s: an app gets a temporary address, so it needs a login", tmpl.Slug)
			}
		}
	}
	for slug := range classes {
		t.Errorf("%s is classified but not in the catalog", slug)
	}
}

// Reset covers app logins only (D-160): a database root or an API key
// other services connect with is shown once and never reset.
func TestAdminResetAppLoginsOnly(t *testing.T) {
	want := map[string]bool{"grafana": true, "umami": true, "pgadmin": true, "rabbitmq": true, "minio": false}
	for _, tmpl := range catalog.All() {
		byCommand, ok := want[tmpl.Slug]
		delete(want, tmpl.Slug)
		r := tmpl.AdminReset
		if (r != nil) != ok {
			t.Errorf("%s: AdminReset set is %v, want %v", tmpl.Slug, r != nil, ok)
			continue
		}
		if tmpl.AdminPassword != "" && tmpl.AdminUser == "" && tmpl.AdminUserFixed == "" && tmpl.Slug != "typesense" { // an API key, no user
			t.Errorf("%s: name the admin login's user (AdminUser or AdminUserFixed)", tmpl.Slug)
		}
		if u := tmpl.AdminUser; u != "" && !slices.ContainsFunc(tmpl.EnvVarSpecs, func(s catalog.EnvVarSpec) bool { return s.Key == u && !s.IsSecret }) {
			t.Errorf("%s: AdminUser %s is not a plain variable spec", tmpl.Slug, u)
		}
		if r == nil {
			continue
		}
		if (r.Argv != nil) != byCommand || (r.Argv != nil) != (r.Stdin != nil) {
			t.Errorf("%s: Argv and Stdin set is %v/%v, want %v", tmpl.Slug, r.Argv != nil, r.Stdin != nil, byCommand)
			continue
		}
		if r.Argv == nil {
			continue
		}
		const pw = "pw-in-argv-is-a-leak"
		if strings.Contains(strings.Join(r.Argv("someone"), " "), pw) || !strings.Contains(r.Stdin(pw), pw) {
			t.Errorf("%s: the password goes on stdin, never in the command", tmpl.Slug)
		}
	}
	for slug := range want {
		t.Errorf("%s: not in the catalog", slug)
	}
	umami, _ := catalog.Get("umami")
	if got := umami.AdminReset.Stdin(`a'b\c`); !strings.HasPrefix(got, `\set pw 'a''b\\c'`+"\n") {
		t.Errorf("umami's psql variable is quoted as %q", got)
	}
}
