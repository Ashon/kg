// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package ssh

import (
	"strings"
	"testing"
)

// install -d sets the mode of a directory that already exists. The parent here
// is sometimes one the system owns: writing a script to /tmp with
// `install -d -m 0755` takes /tmp from 1777 to 0755, and from then on every
// unprivileged process on that machine that wants a temporary file fails. apt
// is one, and it reports every repository as unsigned without saying why.
func TestWriteFileLeavesAnExistingDirectoryAlone(t *testing.T) {
	cmd := writeFileCommand("/tmp/kgenesis-reset.sh", "0700")

	if strings.Contains(cmd, "install -d") {
		t.Errorf("the command sets the mode of the parent directory:\n  %s", cmd)
	}
	if !strings.Contains(cmd, "mkdir -p '/tmp'") {
		t.Errorf("the command does not create the parent:\n  %s", cmd)
	}
	if !strings.Contains(cmd, "chmod '0700' '/tmp/kgenesis-reset.sh'") {
		t.Errorf("the command does not set the file's own mode:\n  %s", cmd)
	}
}
