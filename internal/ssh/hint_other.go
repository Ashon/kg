// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

//go:build !darwin

package ssh

// DialHint has nothing to add away from macOS, where a connection refused by
// policy is reported as an ordinary routing failure.
func DialHint(error) string { return "" }
