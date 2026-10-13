package main

import "runtime/debug"

// version is set by release builds: -ldflags "-X main.version=1.2.3".
var version string

func appVersion() string {
	if version != "" {
		return version
	}
	// "go install module@v1.2.3" records the module version.
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
