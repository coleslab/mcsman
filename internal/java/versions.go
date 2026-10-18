// Package java resolves and installs the Java runtime each server needs.
package java

import (
	"strconv"
	"strings"
)

// MajorFor returns the Java major version required by a Minecraft version.
func MajorFor(mcVersion string) int {
	parts := strings.Split(mcVersion, ".")
	// Year-based versions (26.1 onwards) require Java 25.
	if major, _ := strconv.Atoi(parts[0]); major >= 26 {
		return 25
	}
	minor, patch := 0, 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	if len(parts) > 2 {
		patch, _ = strconv.Atoi(parts[2])
	}
	switch {
	case minor > 20 || (minor == 20 && patch >= 5):
		return 21
	case minor >= 18:
		return 17
	case minor == 17:
		return 16
	default:
		return 8
	}
}
