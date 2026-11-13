package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseMemoryMB(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"512m", 512},
		{"1g", 1024},
		{"2G", 2048},
		{"256M", 256},
		{"", 0},
		{"0.5g", 512},
		{"1024k", 1},
		{"1048576", 1}, // bare bytes
		{"abc", 0},     // non-numeric returns 0
		{"m", 0},       // suffix only returns 0
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			assert.InDelta(t, tc.want, ParseMemoryMB(tc.input), 0.1)
		})
	}
}
