package code

import (
	"crypto/rsa"
	"strings"

	"golang.org/x/crypto/ssh"
)

// parseKey reads one authorized_keys line (an id_*.pub file) and returns the
// key without options or comment, its SHA256 fingerprint and its type.
// Certificates, DSA and RSA under 2048 bits are refused.
func parseKey(line string) (normalized, fingerprint, keyType string, err error) {
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil || len(strings.TrimSpace(string(rest))) > 0 {
		return "", "", "", ErrInvalidKey
	}
	switch pub.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoSKED25519,
		ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoSKECDSA256:
	case ssh.KeyAlgoRSA:
		ck, ok := pub.(ssh.CryptoPublicKey)
		if !ok {
			return "", "", "", ErrInvalidKey
		}
		if k, ok := ck.CryptoPublicKey().(*rsa.PublicKey); !ok || k.N.BitLen() < 2048 {
			return "", "", "", ErrWeakKey
		}
	case ssh.KeyAlgoDSA:
		return "", "", "", ErrWeakKey
	default:
		return "", "", "", ErrInvalidKey
	}
	normalized = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	return normalized, ssh.FingerprintSHA256(pub), pub.Type(), nil
}
