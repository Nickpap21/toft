package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// firewallHost is a host for the host firewall step: has* say what it has,
// and restarting toft-firewall loads the table.
type firewallHost struct {
	hasNft, enabled, loaded bool
	rules, unit             string // the files' contents now
}

func (h *firewallHost) shell() *fakeShell {
	return &fakeShell{answer: func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "command -v nft"):
			if h.hasNft {
				return "yes"
			}
			return "no"
		case strings.HasPrefix(cmd, "sha256sum "+hostFirewallRulesPath):
			return sha256Hex(h.rules)
		case strings.HasPrefix(cmd, "sha256sum "+hostFirewallUnitPath):
			return sha256Hex(h.unit)
		case strings.HasPrefix(cmd, "systemctl is-enabled toft-firewall"):
			if h.enabled {
				return "enabled"
			}
			return "disabled"
		case cmd == "systemctl enable toft-firewall":
			h.enabled = true
		case cmd == "systemctl restart toft-firewall":
			h.loaded = true
		case strings.HasPrefix(cmd, "nft list table ip toft"):
			if h.loaded {
				return "loaded"
			}
			return "missing"
		}
		return ""
	}}
}

func TestEnsureHostFirewallOnAFreshHost(t *testing.T) {
	h := &firewallHost{}
	sh := h.shell()
	var lines []string
	require.NoError(t, ensureHostFirewall(sh, func(l string) { lines = append(lines, l) }))

	assert.True(t, issued(sh.cmds, "apt-get install -yq nftables"))
	order := []string{"nft -c -f -", "cat > " + hostFirewallRulesPath, "cat > " + hostFirewallUnitPath,
		"systemctl daemon-reload", "systemctl enable toft-firewall", "systemctl restart toft-firewall"}
	at := -1
	for _, want := range order {
		i := indexOf(sh.cmds, want)
		require.GreaterOrEqual(t, i, 0, "%q is run", want)
		assert.Greater(t, i, at, "%q runs after the step before it: the rules are checked before anything is written", want)
		at = i
	}
	assert.Equal(t, []string{hostFirewallRules, hostFirewallRules, hostFirewallUnit}, sh.inputs,
		"the rules are checked, then written, over stdin")
	assert.Contains(t, lines[len(lines)-1], "Host firewall loaded")
}

func TestEnsureHostFirewallChangesNothingOnAHostThatHasIt(t *testing.T) {
	h := &firewallHost{hasNft: true, enabled: true, loaded: true, rules: hostFirewallRules, unit: hostFirewallUnit}
	sh := h.shell()
	var lines []string
	require.NoError(t, ensureHostFirewall(sh, func(l string) { lines = append(lines, l) }))
	for _, m := range mutating {
		assert.False(t, issued(sh.cmds, m), "must not run %q", m)
	}
	assert.Equal(t, []string{"Host firewall already loaded"}, lines)
}

func TestEnsureHostFirewallReloadsOnlyWhatChanged(t *testing.T) {
	t.Run("new rules: written and loaded, the unit left alone", func(t *testing.T) {
		h := &firewallHost{hasNft: true, enabled: true, loaded: true, rules: "old rules", unit: hostFirewallUnit}
		sh := h.shell()
		require.NoError(t, ensureHostFirewall(sh, func(string) {}))
		assert.True(t, issued(sh.cmds, "cat > "+hostFirewallRulesPath))
		assert.True(t, issued(sh.cmds, "systemctl restart toft-firewall"))
		assert.False(t, issued(sh.cmds, "cat > "+hostFirewallUnitPath))
		assert.False(t, issued(sh.cmds, "systemctl daemon-reload"))
	})
	t.Run("table gone (nftables flushed): loaded again, nothing written", func(t *testing.T) {
		h := &firewallHost{hasNft: true, enabled: true, rules: hostFirewallRules, unit: hostFirewallUnit}
		sh := h.shell()
		require.NoError(t, ensureHostFirewall(sh, func(string) {}))
		assert.True(t, issued(sh.cmds, "systemctl restart toft-firewall"))
		assert.False(t, issued(sh.cmds, "cat > "))
	})
	t.Run("unit disabled: enabled, nothing else", func(t *testing.T) {
		h := &firewallHost{hasNft: true, loaded: true, rules: hostFirewallRules, unit: hostFirewallUnit}
		sh := h.shell()
		require.NoError(t, ensureHostFirewall(sh, func(string) {}))
		assert.True(t, issued(sh.cmds, "systemctl enable toft-firewall"))
		assert.False(t, issued(sh.cmds, "systemctl restart toft-firewall"))
	})
}

// refusingShell fails every command containing refuse.
type refusingShell struct {
	fakeShell
	refuse string
}

func (r *refusingShell) Exec(cmd string) (string, error) {
	out, _ := r.fakeShell.Exec(cmd)
	if strings.Contains(cmd, r.refuse) {
		return "Error: syntax error", errors.New("exit status 1")
	}
	return out, nil
}

func (r *refusingShell) ExecInput(cmd string, stdin io.Reader) (string, error) {
	in, _ := io.ReadAll(stdin)
	r.inputs = append(r.inputs, string(in))
	return r.Exec(cmd)
}

func (r *refusingShell) ExecStream(cmd string, logFn func(string)) error {
	_, err := r.Exec(cmd)
	return err
}

func TestEnsureHostFirewallTouchesNothingWhenTheKernelRefusesTheRules(t *testing.T) {
	sh := &refusingShell{refuse: "nft -c"}
	sh.answer = (&firewallHost{hasNft: true}).shell().answer
	err := ensureHostFirewall(sh, func(string) {})
	require.ErrorContains(t, err, "nothing was changed")
	for _, m := range mutating {
		assert.False(t, issued(sh.cmds, m), "must not run %q", m)
	}
}

func TestEnsureHostFirewallFailsWhenTheTableDoesNotLoad(t *testing.T) {
	sh := &fakeShell{answer: func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "command -v nft"):
			return "yes"
		case strings.HasPrefix(cmd, "nft list table ip toft"):
			return "missing" // the unit ran, but the table never appears
		}
		return ""
	}}
	assert.ErrorContains(t, ensureHostFirewall(sh, func(string) {}), "table is not loaded")
}

// Fail-open (D-37): a refused host firewall never fails Provision; the log
// says what stays open, and the run finishes with a warning naming it.
func TestProvisionCarriesOnWhenTheHostFirewallFails(t *testing.T) {
	sh := &refusingShell{refuse: "nft -c"}
	sh.answer = (&provisionedHost{}).answer
	var lines []string
	warnings, err := provision(sh, provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22}, func(l string) { lines = append(lines, l) })
	require.NoError(t, err)
	assert.Equal(t, []string{"the host firewall for app containers"}, warnings)
	assert.True(t, containsLine(lines, "Warning: app containers can reach this host's services"))
	assert.NotContains(t, lines, "Server provisioned successfully!")
	assert.Equal(t, "Server provisioned with warnings, missing the host firewall for app containers: fix what the warnings above say, then Re-provision", lines[len(lines)-1])
}

func indexOf(cmds []string, fragment string) int {
	for i, c := range cmds {
		if strings.Contains(c, fragment) {
			return i
		}
	}
	return -1
}

func containsLine(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}
