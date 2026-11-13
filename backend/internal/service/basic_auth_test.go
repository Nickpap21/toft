package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestBasicAuthPasswordReadsEncryptedAndPlainText(t *testing.T) {
	r := &PostgresRepository{keys: crypto.NewKeys("test-key", "")}
	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	v1, err := r.keys.Seal(credentialAD("basic_auth_password", id), "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	for stored, want := range map[string]string{
		v1:         "s3cret",
		"s3cret":   "s3cret",   // plain text from before 0019 or an older backend
		"YWJjZA==": "YWJjZA==", // plain text that happens to be base64
		"":         "",
	} {
		if got := r.basicAuthPassword(id, stored); got != want {
			t.Errorf("basicAuthPassword(%q) = %q, want %q", stored, got, want)
		}
	}
}

func TestServiceJSONHasNoBasicAuthPassword(t *testing.T) {
	b, err := json.Marshal(Service{BasicAuthUser: "admin", BasicAuthPassword: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "basic_auth_password") {
		t.Errorf("service JSON carries the basic auth password: %s", b)
	}
}
