package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/internal/instance"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"golang.org/x/crypto/ssh"
)

// gitHostKeySetting holds git's SSH host key, encrypted with ENCRYPTION_KEY,
// in instance_settings: in the database's backup, so a restored panel keeps
// the key its users' known_hosts pinned (D-71).
const gitHostKeySetting = "git_ssh_host_key"

// gitHostKeyAD is where it is sealed for (D-126).
var gitHostKeyAD = crypto.AD("instance_settings.value", gitHostKeySetting)

type settingStore interface {
	GetSetting(ctx context.Context, key string) ([]byte, error)
	MergeSetting(ctx context.Context, key string, value []byte, actorID string) error
}

// gitHostKey reads git's SSH host key, or makes an ed25519 one on the
// first start.
func gitHostKey(ctx context.Context, settings settingStore, keys *crypto.Keys) (ssh.Signer, error) {
	var stored struct {
		Key string `json:"key"`
	}
	raw, err := settings.GetSetting(ctx, gitHostKeySetting)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &stored); err != nil {
			return nil, fmt.Errorf("decode git host key: %w", err)
		}
		pemKey, err := keys.Open(gitHostKeyAD, stored.Key)
		if err != nil {
			return nil, fmt.Errorf("decrypt git host key: %w", err)
		}
		return ssh.ParsePrivateKey([]byte(pemKey))
	case !errors.Is(err, instance.ErrNotFound):
		return nil, fmt.Errorf("read git host key: %w", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate git host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "toft git")
	if err != nil {
		return nil, fmt.Errorf("encode git host key: %w", err)
	}
	if stored.Key, err = keys.Seal(gitHostKeyAD, string(pem.EncodeToMemory(block))); err != nil {
		return nil, fmt.Errorf("encrypt git host key: %w", err)
	}
	value, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	if err := settings.MergeSetting(ctx, gitHostKeySetting, value, ""); err != nil {
		return nil, fmt.Errorf("store git host key: %w", err)
	}
	return ssh.NewSignerFromKey(priv)
}
