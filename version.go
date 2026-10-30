package main

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// version is CardHub's release number. Release builds can stamp it with
// -ldflags "-X main.version=1.2.3"; bump it here for each release.
var version = "0.6.0"

// versionLabel is what the interface shows: "v" and the number, e.g. "v0.1.0".
// A copy installed from a tagged release (go install …@v0.2.0) shows that tag.
func versionLabel() string {
	if info, ok := debug.ReadBuildInfo(); ok && isRelease(info.Main.Version) {
		return info.Main.Version
	}
	return "v" + strings.TrimPrefix(version, "v")
}

// pseudoVersion matches the versions Go gives untagged commits, like
// v0.0.0-20261016194020-95a00727c30f.
var pseudoVersion = regexp.MustCompile(`-(0\.)?\d{14}-[0-9a-f]{12}`)

// isRelease reports whether v is a clean tagged version (not a local or
// untagged build).
func isRelease(v string) bool {
	return strings.HasPrefix(v, "v") && !pseudoVersion.MatchString(v) && !strings.Contains(v, "+dirty")
}
