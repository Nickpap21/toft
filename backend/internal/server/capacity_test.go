package server_test

import (
	"testing"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/stretchr/testify/assert"
)

func TestServerEffectiveCapacity(t *testing.T) {
	tests := []struct {
		name    string
		s       server.Server
		wantCPU float64
		wantMem int64
	}{
		{
			name:    "uses discovered when no override",
			s:       server.Server{TotalCPUCores: floatPtr(4.0), TotalMemoryMB: int64Ptr(8192)},
			wantCPU: 4.0, wantMem: 8192,
		},
		{
			name:    "uses override when both set",
			s:       server.Server{TotalCPUCores: floatPtr(4.0), TotalMemoryMB: int64Ptr(8192), CPUCoresOverride: floatPtr(2.0), MemoryMBOverride: int64Ptr(4096)},
			wantCPU: 2.0, wantMem: 4096,
		},
		{
			name:    "zero when nothing discovered and no override",
			s:       server.Server{},
			wantCPU: 0, wantMem: 0,
		},
		{
			name:    "cpu override only, memory falls back to discovered",
			s:       server.Server{TotalCPUCores: floatPtr(4.0), TotalMemoryMB: int64Ptr(8192), CPUCoresOverride: floatPtr(1.0)},
			wantCPU: 1.0, wantMem: 8192,
		},
		{
			name:    "memory override only, cpu falls back to discovered",
			s:       server.Server{TotalCPUCores: floatPtr(4.0), TotalMemoryMB: int64Ptr(8192), MemoryMBOverride: int64Ptr(2048)},
			wantCPU: 4.0, wantMem: 2048,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantCPU, tc.s.EffectiveCPUCores())
			assert.Equal(t, tc.wantMem, tc.s.EffectiveMemoryMB())
		})
	}
}

func floatPtr(f float64) *float64 { return &f }
func int64Ptr(i int64) *int64     { return &i }
