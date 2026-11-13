package envvar

import (
	"errors"
	"fmt"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/listversion"
)

// ErrKeyIsSecret refuses a plain variable whose key an environment secret
// already uses: saving it would put the secret's key in plaintext.
var ErrKeyIsSecret = errors.New("a secret already uses this key; delete the secret first")

// ErrPreviewService: a preview's copy reads its variables from its original
// at each deploy, so its own are never written.
var ErrPreviewService = errors.New("a preview's services read their variables and secrets from the service they copy: change the original")

// ErrNotFound is a secret that does not exist.
var ErrNotFound = errors.New("secret not found")

// EnvVar represents a runtime environment variable injected into a service
// deployment: a service's own (ServiceID set) or one its environment shares
// with every service in it (EnvironmentID set).
type EnvVar struct {
	ID            string `json:"id"`
	ServiceID     string `json:"service_id,omitempty"`
	EnvironmentID string `json:"environment_id,omitempty"`
	Key           string `json:"key"`
	Value         string `json:"value"`
	IsSecret      bool   `json:"is_secret"`
	// Masked: shown as dots, stored and returned readable (0023). Service
	// variables only.
	Masked    bool      `json:"masked"`
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is when its value (or mask) last changed; a save that
	// leaves it as it was keeps it.
	UpdatedAt time.Time `json:"updated_at"`
	// UpdatedBy is who set its value: a name, an agent's "Claude Code for
	// Ada" (D-100), "" when not recorded.
	UpdatedBy string `json:"updated_by"`
}

// SecretDTO is the public representation of a secret. The value is never exposed.
type SecretDTO struct {
	ID            string    `json:"id"`
	ServiceID     string    `json:"service_id,omitempty"`
	EnvironmentID string    `json:"environment_id,omitempty"`
	Key           string    `json:"key"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// UpdatedBy is who last set it: a name, an agent's "Claude Code for
	// Ada" (D-100), "" when not recorded.
	UpdatedBy string `json:"updated_by"`
}

type UpsertInput struct {
	Key   string `json:"key" binding:"required,max=255"`
	Value string `json:"value" binding:"required,max=32768"`
	// Masked hides the value on screen; it stays readable. Ignored for an
	// environment's variables.
	Masked bool `json:"masked"`
}

// CreateSecretInput is the write-once input for creating a secret.
type CreateSecretInput struct {
	Key   string `json:"key" binding:"required,max=255"`
	Value string `json:"value" binding:"required,max=32768"`
}

// BulkUpsertInput replaces all env vars for a service in one atomic operation.
// Version is the one from the list it replaces; it has no binding tag so a
// missing one is its own refusal, version_required.
type BulkUpsertInput struct {
	Vars    []UpsertInput `json:"vars" binding:"required"`
	Version string        `json:"version"`
}

// versionOf says which state of the plain variables a client read, and a
// replace must send it back: a digest of each key and when it last changed
// (to the microsecond, as stored), in key order. Not the values, and not the
// secrets: a re-seal changes a ciphertext without anyone changing a variable,
// and a replace leaves secrets alone. An empty list has its own version.
func versionOf(vars []EnvVar) string {
	lines := make([]string, 0, len(vars))
	for _, v := range vars {
		lines = append(lines, fmt.Sprintf("%q %d", v.Key, v.UpdatedAt.UnixMicro()))
	}
	return listversion.Of(lines)
}
