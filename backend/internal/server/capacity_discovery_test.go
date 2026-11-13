package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseDiscoveryOutput(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantCores float64
		wantMem   int64
		wantErr   bool
	}{
		{
			name:      "valid full output",
			raw:       "cpu:8\nmem:15872\n",
			wantCores: 8, wantMem: 15872,
		},
		{
			name:      "valid with trailing whitespace",
			raw:       "cpu:4 \r\nmem:8192 \r\n",
			wantCores: 4, wantMem: 8192,
		},
		{
			name:    "empty string",
			raw:     "",
			wantErr: true,
		},
		{
			name:    "only cpu key — no memory",
			raw:     "cpu:8\n",
			wantErr: true,
		},
		{
			name:    "only mem key — no cpu",
			raw:     "mem:15872\n",
			wantErr: true,
		},
		{
			name:    "cpu is zero — broken nproc",
			raw:     "cpu:0\nmem:15872\n",
			wantErr: true,
		},
		{
			name:    "mem is zero — broken free",
			raw:     "cpu:8\nmem:0\n",
			wantErr: true,
		},
		{
			name:    "non-numeric cpu",
			raw:     "cpu:abc\nmem:15872\n",
			wantErr: true,
		},
		{
			name:    "non-numeric mem",
			raw:     "cpu:8\nmem:abc\n",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cores, memMB, err := parseDiscoveryOutput(tc.raw)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.wantCores, cores)
			assert.Equal(t, tc.wantMem, memMB)
		})
	}
}
