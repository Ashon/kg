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

	// Image is the controller image this build installs.
	//
	// It travels with the version rather than living only in the embedded
	// manifest, because the two have to agree: a released CLI that installed
	// the manifest's default would send kubelet after a tag that was never
	// published, and the only symptom would be ImagePullBackOff.
	Image = "ghcr.io/ashon/kg:dev"
)

// String is a one line summary suitable for `--version`. The name is the
// binary's own: kgenesis ships a CLI and a controller, and a version line that
// named the project rather than the thing printing it would leave a reader
// guessing which of the two they are looking at.
func String(name string) string {
	commit := GitCommit
	if commit == "" {
		commit = vcsRevision()
	}
	if commit == "" {
		commit = "unknown"
	}

	out := fmt.Sprintf("%s %s (commit %s, %s/%s, %s)",
		name, Version, commit, runtime.GOOS, runtime.GOARCH, runtime.Version())
	if BuildDate != "" {
		out += ", built " + BuildDate
	}
	// The image is part of the answer to "what does this binary do": it is what
	// `init` installs, and the first thing to check when the controller cannot
	// start.
	if Image != "" {
		out += "\ncontroller image " + Image
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
