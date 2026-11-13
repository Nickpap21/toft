package deploypolicy

import (
	"testing"

	"github.com/Nickpap21/toft/backend/internal/composespec"
)

// A value pinned by the plain fingerprint (v0.26.0 and v0.27.0) or the keyed
// one (v0.28.0) is pinned; another key's is not (D-126).
func TestPinMatchesPlainOrKeyedFingerprint(t *testing.T) {
	v := Violation{Rule: composespec.RuleCapAdd, Service: "web", Field: "cap_add[0]", Value: "NET_ADMIN"}
	key, other := []byte("instance-key"), []byte("another-key")
	got := finish([]Violation{v}, nil, "app:1", key)[0]
	if got.Fingerprint == "" || got.AltFingerprint == "" || got.Fingerprint == got.AltFingerprint {
		t.Fatalf("fingerprints %q, %q", got.Fingerprint, got.AltFingerprint)
	}
	v.Image = "app:1"
	for pin, want := range map[string]string{
		fingerprint(v, nil):   StatePinned,
		fingerprint(v, key):   StatePinned,
		fingerprint(v, other): StatePending,
	} {
		e := &Exception{AllowedRules: []Rule{composespec.RuleCapAdd}, AllowedValues: []PinnedValue{{Fingerprint: pin}}}
		if s := e.state(got); s != want {
			t.Errorf("pin %s: %s, want %s", pin, s, want)
		}
	}
	if finish([]Violation{v}, nil, "", nil)[0].AltFingerprint != "" {
		t.Error("a keyed fingerprint without a key")
	}
}
