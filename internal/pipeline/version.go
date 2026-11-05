package pipeline

import (
	"regexp"
	"strconv"
	"strings"
)

// SchemaVersion is the newest pipeline schema this build understands.
const SchemaVersion = 1

var versionPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*))?$`)

// ValidVersion reports whether v is a semantic version (MAJOR.MINOR.PATCH
// with an optional -prerelease).
func ValidVersion(v string) bool {
	return versionPattern.MatchString(v)
}

// CompareVersions orders two valid semantic versions, returning -1, 0, or 1.
// A prerelease sorts before its release.
func CompareVersions(a, b string) int {
	ma, mb := versionPattern.FindStringSubmatch(a), versionPattern.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return strings.Compare(a, b)
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case ma[4] == mb[4]:
		return 0
	case ma[4] == "":
		return 1
	case mb[4] == "":
		return -1
	}
	return comparePrerelease(ma[4], mb[4])
}

func comparePrerelease(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, errX := strconv.Atoi(pa[i])
		y, errY := strconv.Atoi(pb[i])
		switch {
		case errX == nil && errY == nil:
			if x != y {
				if x < y {
					return -1
				}
				return 1
			}
		case errX == nil:
			return -1
		case errY == nil:
			return 1
		default:
			if c := strings.Compare(pa[i], pb[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(pa) < len(pb):
		return -1
	case len(pa) > len(pb):
		return 1
	}
	return 0
}

// NextPatch returns v with its patch number incremented (prerelease dropped).
func NextPatch(v string) string {
	m := versionPattern.FindStringSubmatch(v)
	if m == nil {
		return "0.1.0"
	}
	patch, _ := strconv.Atoi(m[3])
	if m[4] != "" {
		return m[1] + "." + m[2] + "." + m[3]
	}
	return m[1] + "." + m[2] + "." + strconv.Itoa(patch+1)
}
