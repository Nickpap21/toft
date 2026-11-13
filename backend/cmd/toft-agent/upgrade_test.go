package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

// The release key's signature of a binary's manifest verifies for that
// binary, version and architecture only (D-123), and only under the key
// the agent is built with.
func TestManifestSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	stateDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "release.pub"), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("pretend binary"))
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest("v0.24.0", "amd64", sum)))
	if err := verifyManifest("v0.24.0", "amd64", sum, sig); err != nil {
		t.Fatalf("a good signature: %v", err)
	}
	if err := verifyManifest("v0.25.0", "amd64", sum, sig); err == nil {
		t.Fatal("a signed release offered as another version must fail")
	}
	if err := verifyManifest("v0.24.0", "arm64", sum, sig); err == nil {
		t.Fatal("another architecture must fail")
	}
	if err := verifyManifest("v0.24.0", "amd64", sha256.Sum256([]byte("another binary")), sig); err == nil {
		t.Fatal("another binary must fail")
	}
	if err := verifyManifest("v0.24.0", "amd64", sum, ""); err == nil {
		t.Fatal("an upgrade without a manifest signature must fail")
	}
	binarySig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("pretend binary")))
	if err := verifyManifest("v0.24.0", "amd64", sum, binarySig); err == nil {
		t.Fatal("the binary-only signature (agents before v0.24.0) must not pass as a manifest's")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if err := verifyManifest("v0.24.0", "amd64", sum, base64.StdEncoding.EncodeToString(ed25519.Sign(other, manifest("v0.24.0", "amd64", sum)))); err == nil {
		t.Fatal("another key's signature must fail")
	}
}

// openssl's own output, made with release.yml's commands (OpenSSL 3.0.13 on
// Ubuntu 24.04, as the release runner, `openssl pkeyutl -sign -rawin` over
// the printf'd manifest line, `base64 -w0`) for v0.24.0 with a throwaway
// key whose private half was never kept: testdata/openssl.
func TestOpenSSLManifestSignatureVerifies(t *testing.T) {
	dir := filepath.Join("testdata", "openssl")
	// A copy: removeSelf deletes stateDir, never the fixture.
	pub, err := os.ReadFile(filepath.Join(dir, "release.pub"))
	if err != nil {
		t.Fatal(err)
	}
	stateDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "release.pub"), pub, 0o644); err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(filepath.Join(dir, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bin)
	for _, arch := range []string{"amd64", "arm64"} {
		sig, err := os.ReadFile(filepath.Join(dir, "bin.manifest-"+arch+".sig"))
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyManifest("v0.24.0", arch, sum, string(sig)); err != nil {
			t.Fatalf("%s: %v", arch, err)
		}
		if err := verifyManifest("v0.24.1", arch, sum, string(sig)); err == nil {
			t.Fatalf("%s: another version must fail", arch)
		}
	}
}

func TestNoReleaseKeyRefusesUpgrades(t *testing.T) {
	stateDir = t.TempDir()
	saved := embeddedReleaseKey
	embeddedReleaseKey = "none"
	defer func() { embeddedReleaseKey = saved }()
	if err := verifyManifest("v0.24.0", "amd64", [32]byte{}, "AAAA"); err == nil {
		t.Fatal("an agent without a release key must refuse every upgrade")
	}
}
