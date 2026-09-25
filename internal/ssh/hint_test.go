// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package ssh_test

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/Ashon/kg/internal/ssh"
)

// macOS reports a local network denial as EHOSTUNREACH, which is what a genuine
// routing failure looks like. Without the note, the error sends people hunting
// for a network problem that is not there - it cost hours before it was written.
func TestDialHint(t *testing.T) {
	wrapped := fmt.Errorf("dial 192.168.105.11:22: %w", syscall.EHOSTUNREACH)

	hint := ssh.DialHint(wrapped)
	if runtime.GOOS != "darwin" {
		if hint != "" {
			t.Errorf("a hint was offered away from macOS: %q", hint)
		}
		return
	}

	for _, want := range []string{"Local Network", "System Settings"} {
		if !strings.Contains(hint, want) {
			t.Errorf("the hint does not mention %q:\n%s", want, hint)
		}
	}

	// Anything else is an ordinary failure and must not be explained away.
	for _, other := range []error{
		syscall.ECONNREFUSED,
		syscall.ETIMEDOUT,
		errors.New("ssh: handshake failed"),
	} {
		if got := ssh.DialHint(fmt.Errorf("dial: %w", other)); got != "" {
			t.Errorf("%v produced a local network hint: %q", other, got)
		}
	}
}
