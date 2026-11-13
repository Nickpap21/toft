package deployment

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRoleCanBypass(t *testing.T) {
	tests := []struct {
		memberRole string
		bypassMin  string
		want       bool
	}{
		{"owner", "owner", true},
		{"developer", "owner", false},
		{"developer", "developer", true},
		{"viewer", "developer", false},
		{"owner", "developer", true},
	}
	for _, tc := range tests {
		t.Run(tc.memberRole+"_bypass_"+tc.bypassMin, func(t *testing.T) {
			assert.Equal(t, tc.want, roleCanBypass(tc.memberRole, tc.bypassMin))
		})
	}
}
