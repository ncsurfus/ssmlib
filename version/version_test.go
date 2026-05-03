package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSupportsVersion(t *testing.T) {
	flag := FeatureFlag("3.0.196.0")

	tests := []struct {
		name     string
		agent    string
		expected bool
	}{
		{"exact match", "3.0.196.0", true},
		{"newer patch", "3.0.197.0", true},
		{"newer minor", "3.1.0.0", true},
		{"newer major", "4.0.0.0", true},
		{"older patch", "3.0.195.0", false},
		{"older minor", "2.9.999.0", false},
		{"older major", "2.0.196.0", false},
		{"component count mismatch (3)", "3.0.196", false},
		{"component count mismatch (5)", "3.0.196.0.1", false},
		{"empty string", "", false},
		{"non-numeric agent", "3.0.abc.0", false},
		{"non-numeric flag component", "3.0.196.0", true}, // flag itself is valid
		{"newer last component", "3.0.196.1", true},
		{"all zeros agent", "0.0.0.0", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, flag.SupportsVersion(tc.agent))
		})
	}
}

func TestSupportsVersion_NonNumericFlag(t *testing.T) {
	flag := FeatureFlag("3.0.abc.0")
	assert.False(t, flag.SupportsVersion("3.0.196.0"))
}

func TestSupportsVersion_EqualVersions(t *testing.T) {
	flag := FeatureFlag("1.2.3.4")
	assert.True(t, flag.SupportsVersion("1.2.3.4"))
}
