package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/internal/catalog"
	"github.com/Nickpap21/toft/backend/internal/envvar"
)

// Transactor runs fn in one transaction, carried by its context. Satisfied
// by dbtx.Runner.
type Transactor interface {
	InTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// WithTransactor lets a create store a service with its secrets in one
// transaction. Called from main.go.
func (s *ServiceManager) WithTransactor(t Transactor) { s.tx = t }

// Login is a basic auth user and password.
type Login struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

// withGenerated adds what a catalog service's template gives it to in, for
// each spec in.Variables and in.Secrets don't give: a secret as the spec's
// Generate says, a plain variable at its default (one given empty stays
// empty); and, for a SetupLogin template without basic auth, a temporary
// login. It returns the secrets and the login too, for the response: the
// only time they are shown (secrets are write-once).
func withGenerated(in CreateInput) (CreateInput, []KeyValue, *Login) {
	tmpl, ok := catalog.Get(in.AddonSlug)
	if in.AddonSlug == "" || !ok {
		return in, nil, nil
	}
	given := make(map[string]bool, len(in.Variables)+len(in.Secrets))
	for _, key := range givenKeys(in) {
		given[key] = true
	}
	var generated []KeyValue
	in.Variables = append([]Variable{}, in.Variables...)
	for _, spec := range tmpl.EnvVarSpecs {
		switch {
		case given[spec.Key]:
		case spec.IsSecret:
			generated = append(generated, KeyValue{Key: spec.Key, Value: generatedSecret(spec.Generate)})
		case spec.Default != "":
			in.Variables = append(in.Variables, Variable{Key: spec.Key, Value: spec.Default})
		}
	}
	in.Secrets = append(append([]KeyValue{}, in.Secrets...), generated...)
	// An IP allowlist alone still lets anyone in its ranges claim the app.
	var login *Login
	if tmpl.SetupLogin && in.BasicAuthUser == "" && in.BasicAuthPassword == "" {
		login = &Login{User: catalog.TemporaryLoginUser, Password: generatedSecret("")}
		in.BasicAuthUser, in.BasicAuthPassword = login.User, login.Password
	}
	return in, generated, login
}

// generatedSecret is a secret's value as the browser makes it (catalog
// EnvVarSpec.Generate): "" 24 characters, "long" 64, from a set without
// look-alikes; "hex64" 32 random bytes in hex.
func generatedSecret(generate string) string {
	if generate == "hex64" {
		b := make([]byte, 32)
		_, _ = rand.Read(b) // never fails (crypto/rand, Go 1.24+)
		return hex.EncodeToString(b)
	}
	const set = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	n := 24
	if generate == "long" {
		n = 64
	}
	out := make([]byte, 0, n)
	b := make([]byte, 1)
	for len(out) < n {
		_, _ = rand.Read(b)
		// A byte past the last whole multiple of the set is drawn again, so
		// every character is equally likely.
		if int(b[0]) < 256-256%len(set) {
			out = append(out, set[int(b[0])%len(set)])
		}
	}
	return string(out)
}

// givenKeys are the keys of the variables and secrets a create gives.
func givenKeys(in CreateInput) []string {
	keys := make([]string, 0, len(in.Variables)+len(in.Secrets))
	for _, v := range in.Variables {
		keys = append(keys, v.Key)
	}
	for _, s := range in.Secrets {
		keys = append(keys, s.Key)
	}
	return keys
}

// checkKeysOnce refuses a key given twice: a variable and a secret share
// the service's keys.
func checkKeysOnce(in CreateInput) error {
	keys := make(map[string]bool, len(in.Variables)+len(in.Secrets))
	for _, key := range givenKeys(in) {
		if keys[key] {
			return fmt.Errorf("%w: %s is given twice", ErrInvalidConfig, key)
		}
		keys[key] = true
	}
	return nil
}

// RevealKeeper keeps a catalog app's admin password, sealed, for the
// person who created the service or reset it (D-160). Forget deletes every
// copy of key (the temporary login's: ""), once it no longer opens the app.
// Satisfied by reveal.Repository.
type RevealKeeper interface {
	Keep(ctx context.Context, serviceID, userID, key, value, reason string) error
	Forget(ctx context.Context, serviceID, key string) error
}

// WithReveals lets a create keep its admin password for its creator.
// Called from main.go.
func (s *ServiceManager) WithReveals(k RevealKeeper) { s.reveals = k }

// adminLogin is what a create keeps for its creator (D-160): its template's
// admin password among the secrets it stores, or its temporary login (key
// ""), generated here (withGenerated) or by the new-service form; nil for
// neither.
func adminLogin(in CreateInput) *KeyValue {
	tmpl, ok := catalog.Get(in.AddonSlug)
	if in.AddonSlug == "" || !ok {
		return nil
	}
	if tmpl.SetupLogin && in.BasicAuthUser == catalog.TemporaryLoginUser && in.BasicAuthPassword != "" {
		return &KeyValue{Value: in.BasicAuthPassword}
	}
	if tmpl.AdminPassword == "" {
		return nil
	}
	for _, kv := range in.Secrets {
		if kv.Key == tmpl.AdminPassword {
			return &kv
		}
	}
	return nil
}

// keepAdminLogin keeps login for userID, in the create's transaction.
func (s *ServiceManager) keepAdminLogin(ctx context.Context, serviceID, userID string, login *KeyValue) error {
	if login == nil || s.reveals == nil {
		return nil
	}
	if err := s.reveals.Keep(ctx, serviceID, userID, login.Key, login.Value, "created"); err != nil {
		return fmt.Errorf("keep its admin password: %w", err)
	}
	return nil
}

// createWithSecrets runs create and stores in's variables and secrets on
// the service it made, and its admin login for its creator, in one
// transaction: none is stored unless all are. With none of them it is
// create alone.
func (s *ServiceManager) createWithSecrets(ctx context.Context, userID string, in CreateInput,
	create func(ctx context.Context) (*Service, error)) (*Service, error) {
	if len(in.Variables) == 0 && len(in.Secrets) == 0 && (adminLogin(in) == nil || s.reveals == nil) {
		return create(ctx)
	}
	if s.tx == nil || s.variables == nil {
		return nil, errors.New("create a service with variables or secrets: no transactor or variables")
	}
	var svc *Service
	if err := s.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if svc, err = create(ctx); err != nil {
			return err
		}
		return s.writeGiven(ctx, svc.ID, userID, in)
	}); err != nil {
		return nil, err
	}
	return svc, nil
}

// writeGiven stores a new service's variables, its secrets (write-once) and
// its admin login for its creator, in the create's transaction.
func (s *ServiceManager) writeGiven(ctx context.Context, serviceID, userID string, in CreateInput) error {
	if len(in.Variables) > 0 {
		vars := make([]envvar.UpsertInput, 0, len(in.Variables))
		for _, v := range in.Variables {
			vars = append(vars, envvar.UpsertInput{Key: v.Key, Value: v.Value})
		}
		if err := s.variables.Seed(ctx, serviceID, userID, vars); err != nil {
			return fmt.Errorf("its variables: %w", err)
		}
	}
	for _, sec := range in.Secrets {
		if _, err := s.variables.CreateSecret(ctx, serviceID, userID, envvar.CreateSecretInput{Key: sec.Key, Value: sec.Value}); err != nil {
			return fmt.Errorf("its secret %s: %w", sec.Key, err)
		}
	}
	return s.keepAdminLogin(ctx, serviceID, userID, adminLogin(in))
}
