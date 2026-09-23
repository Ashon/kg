// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

//go:build darwin

package ssh

import (
	"errors"
	"syscall"
)

// DialHint explains EHOSTUNREACH on macOS.
//
// macOS gates access to the local network per application and reports a denial
// as EHOSTUNREACH, which is indistinguishable from a genuine routing failure.
// Apple's own binaries are exempt, so ping and nc reach a host from the same
// shell where kgenesis cannot connect at all. Without this note the error sends
// people looking for a network problem that is not there.
func DialHint(err error) string {
	if !errors.Is(err, syscall.EHOSTUNREACH) {
		return ""
	}
	return "These hosts answer ping and nc from this shell, so macOS is denying kgenesis\n" +
		"access to the local network rather than the route being missing. Apple's own\n" +
		"binaries are exempt, which is why the other tools reach them.\n\n" +
		"  System Settings -> Privacy & Security -> Local Network\n\n" +
		"Enable the terminal you are running from, then try again."
}
