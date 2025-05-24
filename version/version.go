package version

import (
	"strconv"
	"strings"
)

// FeatureFlag determines if a feature flag is enabled by checking if the
// version is **greater** than set version.
type FeatureFlag string

func (v FeatureFlag) SupportsVersion(agentVersion string) bool {
	thisVersionSlice := strings.Split(string(v), ".")
	agentVersionSlice := strings.Split(agentVersion, ".")

	// Generally all agents should consist of 4 component: 1.0.0.0. If there's a mismatch
	// then the versions simply aren't comparable.
	if len(thisVersionSlice) != len(agentVersionSlice) {
		return false
	}

	// Compare the size of each component. If anything cannot be converted to a number, then it
	// must not comparable to our version.
	// 1.2.3.401 vs 1.2.3.523
	// eg: (1 vs 1), (2 vs 2), (3 vs 3), (401 vs 523)
	for i := range len(thisVersionSlice) {
		thisVersionComponent, err := strconv.Atoi(thisVersionSlice[i])
		if err != nil {
			return false
		}

		agentVersionComponent, err := strconv.Atoi(agentVersionSlice[i])
		if err != nil {
			return false
		}

		if agentVersionComponent > thisVersionComponent {
			// Newer Version
			return true
		} else if agentVersionComponent < thisVersionComponent {
			// Old Version
			return false
		}
	}

	// Equal Version
	return false
}
