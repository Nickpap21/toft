package org_test

import (
	"testing"

	"github.com/Nickpap21/toft/backend/internal/org"
	"github.com/stretchr/testify/assert"
)

func TestValidateBypassRole(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		wantErr bool
	}{
		{"owner is valid", "owner", false},
		{"developer is valid", "developer", false},
		{"viewer is rejected", "viewer", true},
		{"empty is rejected", "", true},
		{"unknown role is rejected", "superadmin", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := org.ValidateBypassRole(tc.role)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
