package sandbox

import (
	"runtime/debug"
	"strings"
)

// Release builds can set these with go build -ldflags -X. go install obtains
// the version from module metadata; local builds use Go's VCS build settings.
var version, revision string

func buildVersion() string {
	info, _ := debug.ReadBuildInfo()
	return formatBuildVersion(info, version, revision)
}

func formatBuildVersion(info *debug.BuildInfo, version, revision string) string {
	dirty := false
	if info != nil {
		if version == "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if revision == "" {
					revision = setting.Value
				}
			case "vcs.modified":
				dirty = setting.Value == "true"
			}
		}
	}
	if version == "" {
		version = "devel"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	parts := []string{"mailarky", version}
	if revision != "" && !strings.HasSuffix(strings.TrimSuffix(version, "+dirty"), "-"+revision) {
		parts = append(parts, revision)
	}
	if dirty && !strings.HasSuffix(version, "+dirty") {
		parts = append(parts, "dirty")
	}
	return strings.Join(parts, " ")
}
