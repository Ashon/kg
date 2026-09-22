// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package version carries the build stamp injected at link time.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set with -ldflags at build time; see the Makefile.
var (
	Version   = "dev"
	GitCommit = ""
	BuildDate = ""
)

// String is a one line summary suitable for `--version`.
func String() string {
	commit := GitCommit
	if commit == "" {
		commit = vcsRevision()
	}
	if commit == "" {
		commit = "unknown"
	}

	out := fmt.Sprintf("kgenesis %s (commit %s, %s/%s, %s)",
		Version, commit, runtime.GOOS, runtime.GOARCH, runtime.Version())
	if BuildDate != "" {
		out += ", built " + BuildDate
	}
	return out
}

// vcsRevision recovers the commit from the build info when it was not injected,
// so `go install` builds still report something useful.
func vcsRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			if len(setting.Value) > 12 {
				return setting.Value[:12]
			}
			return setting.Value
		}
	}
	return ""
}
