package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set at build time with -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// resolveVersion fills in missing build metadata from the module and VCS
// information Go embeds, so `go install ...@vX.Y.Z` and plain `go build`
// still report something useful.
func resolveVersion() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
	fromVCS, dirty := commit == "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if commit == "" {
				commit = s.Value
			}
		case "vcs.time":
			if date == "" {
				date = s.Value
			}
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if !fromVCS {
		return
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if dirty && commit != "" {
		commit += "-dirty"
	}
}

func versionString() string {
	s := "codex-usage " + version
	if commit != "" {
		s += " (" + commit
		if date != "" {
			s += ", " + date
		}
		s += ")"
	}
	return fmt.Sprintf("%s %s/%s %s", s, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
