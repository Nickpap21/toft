package crypto

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const (
	keyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	keyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestV1_BoundToItsPlace(t *testing.T) {
	k := NewKeys(keyA, "")
	ad := AD("env_vars.value", "svc", "DATABASE_URL")
	sealed, err := k.Seal(ad, "postgres://secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1."+k.CurrentID()+".") {
		t.Fatalf("sealed = %q", sealed)
	}
	if got, err := k.Open(ad, sealed); err != nil || got != "postgres://secret" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	for _, other := range []string{
		AD("env_vars.value", "svc", "OTHER"),
		AD("env_vars.value", "svc2", "DATABASE_URL"),
		AD("environment_env_vars.value", "svc", "DATABASE_URL"),
		"",
	} {
		if _, err := k.Open(other, sealed); err == nil {
			t.Errorf("opened at %q", other)
		}
	}
}

func TestV1_Tampered(t *testing.T) {
	k := NewKeys(keyA, "")
	ad := AD("services.git_token", "id")
	sealed, _ := k.Seal(ad, "token")
	body := sealed[len("v1."+k.CurrentID()+"."):]
	raw, _ := base64.RawURLEncoding.DecodeString(body)
	for _, pos := range []int{0, len(raw) / 2, len(raw) - 1} { // nonce, ciphertext, tag
		b := append([]byte(nil), raw...)
		b[pos] ^= 1
		if _, err := k.Open(ad, "v1."+k.CurrentID()+"."+base64.RawURLEncoding.EncodeToString(b)); err == nil {
			t.Errorf("opened with byte %d flipped", pos)
		}
	}
	for _, bad := range []string{"v1." + k.CurrentID(), "v1.00000000." + body, "v2." + k.CurrentID() + "." + body} {
		if _, err := k.Open(ad, bad); err == nil {
			t.Errorf("opened %q", bad)
		}
	}
}

func TestOldFormat_OnlyOpenLegacyOpens(t *testing.T) {
	old, err := Encrypt(keyA, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(old, ".") {
		t.Fatalf("old format holds a dot: %q", old)
	}
	ad := AD("x.y", "1")
	if _, err := NewKeys(keyA, "").Open(ad, old); !errors.Is(err, ErrOldFormat) {
		t.Fatalf("Open = %v, want ErrOldFormat", err)
	}
	// Under either key; the place doesn't matter to an old value.
	for _, k := range []*Keys{NewKeys(keyA, ""), NewKeys(keyB, keyA)} {
		if got, err := k.OpenLegacy(AD("other.place", "2"), old); err != nil || got != "s3cret" {
			t.Errorf("OpenLegacy = %q, %v", got, err)
		}
	}
	if _, err := NewKeys(keyB, "").OpenLegacy(ad, old); err == nil {
		t.Error("opened with another key")
	}
	// v1 opens as Open does, bound to its place.
	sealed, _ := NewKeys(keyA, "").Seal(ad, "v")
	if got, err := NewKeys(keyB, keyA).OpenLegacy(ad, sealed); err != nil || got != "v" {
		t.Errorf("OpenLegacy(v1) = %q, %v", got, err)
	}
	if _, err := NewKeys(keyA, "").OpenLegacy(AD("x.y", "2"), sealed); err == nil {
		t.Error("v1 opened at another place")
	}
}

func TestV1_PreviousKeyOpens(t *testing.T) {
	ad := AD("servers.ssh_key", "id")
	sealed, _ := NewKeys(keyA, "").Seal(ad, "pem")
	if got, err := NewKeys(keyB, keyA).Open(ad, sealed); err != nil || got != "pem" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := NewKeys(keyB, "").Open(ad, sealed); err == nil {
		t.Fatal("opened without the key it was sealed with")
	}
}
