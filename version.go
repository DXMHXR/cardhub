package main

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// version is CardHub's release number. Release builds can stamp it with
// -ldflags "-X main.version=1.2.3"; bump it here for each release.
var version = "0.1.0"

// versionLabel is what the interface shows, e.g. "v0.1.0". A build of an
// untagged commit adds the commit (and "-dirty" for uncommitted changes), so
// a local build can be told apart from a release.
func versionLabel() string {
	label := "v" + strings.TrimPrefix(version, "v")
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return label
	}
	if v := info.Main.Version; isRelease(v) {
		return v // installed from a tagged release: go install …@v0.1.0
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if len(rev) >= 7 {
		label += "+" + rev[:7]
		if dirty {
			label += "-dirty"
		}
	}
	return label
}

// pseudoVersion matches the versions Go gives untagged commits, like
// v0.0.0-20261016194020-95a00727c30f.
var pseudoVersion = regexp.MustCompile(`-(0\.)?\d{14}-[0-9a-f]{12}`)

// isRelease reports whether v is a clean tagged version (not a local or
// untagged build).
func isRelease(v string) bool {
	return strings.HasPrefix(v, "v") && !pseudoVersion.MatchString(v) && !strings.Contains(v, "+dirty")
}
