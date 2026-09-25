// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package provisioner

import (
	"io"
	"strings"
	"testing"

	"github.com/Ashon/kg/internal/ssh"
	"github.com/Ashon/kg/internal/ssh/sshtest"
)

// fakeHost models the files the provisioner reads and writes on a host, so the
// state machine can be exercised without a real machine.
type fakeHost struct {
	files map[string]string
	uid   string
	// missing lists commands the preflight should not find.
	missing map[string]bool
}

func newFakeHost() *fakeHost {
	return &fakeHost{files: map[string]string{}, uid: "0", missing: map[string]bool{}}
}

// handle interprets the small shell vocabulary the provisioner actually uses.
func (h *fakeHost) handle(cmd string, stdin io.Reader) (string, string, int) {
	switch {
	case cmd == "id -u":
		return h.uid + "\n", "", 0

	case strings.HasPrefix(cmd, "command -v "):
		name := strings.TrimPrefix(cmd, "command -v ")
		if h.missing[name] {
			return "", "", 1
		}
		return "/usr/bin/" + name + "\n", "", 0

	// WriteFile: "install -d ... && cat > 'PATH' && chmod ..."
	case strings.Contains(cmd, "cat > "):
		data, _ := io.ReadAll(stdin)
		h.files[quotedArgAfter(cmd, "cat > ")] = string(data)
		return "", "", 0

	// Status reads: "if [ -f \"PATH\" ]; then cat \"PATH\"; fi"
	case strings.HasPrefix(cmd, "if [ -f "):
		path := doubleQuotedArgAfter(cmd, "if [ -f ")
		if content, ok := h.files[path]; ok {
			if strings.Contains(cmd, "tail -n") {
				return content, "", 0
			}
			return content, "", 0
		}
		return "", "", 0

	case strings.Contains(cmd, "rm -f"):
		for _, path := range []string{exitCodePath, checksumPath} {
			delete(h.files, path)
		}
		return "", "", 0

	case strings.Contains(cmd, "setsid nohup"):
		return "", "", 0

	default:
		return "", "", 0
	}
}

// quotedArgAfter pulls the single-quoted argument that follows a prefix.
func quotedArgAfter(cmd, prefix string) string {
	rest := cmd[strings.Index(cmd, prefix)+len(prefix):]
	rest = strings.TrimPrefix(rest, "'")
	if i := strings.Index(rest, "'"); i >= 0 {
		return rest[:i]
	}
	return rest
}

func doubleQuotedArgAfter(cmd, prefix string) string {
	rest := cmd[strings.Index(cmd, prefix)+len(prefix):]
	rest = strings.TrimPrefix(rest, `"`)
	if i := strings.Index(rest, `"`); i >= 0 {
		return rest[:i]
	}
	return rest
}

func connect(t *testing.T, host *fakeHost) *ssh.Client {
	t.Helper()
	return sshtest.Connect(t, host.handle)
}

func TestStatusIsIdleOnAFreshHost(t *testing.T) {
	client := connect(t, newFakeHost())

	state, err := Status(t.Context(), client, "abc123")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if state.Phase != PhaseIdle {
		t.Errorf("phase: got %s, want %s", state.Phase, PhaseIdle)
	}
}

func TestStartThenStatusReportsRunningThenSucceeded(t *testing.T) {
	host := newFakeHost()
	client := connect(t, host)

	script := []byte("#!/usr/bin/env bash\necho hello\n")
	checksum := Checksum(script)

	if err := Start(t.Context(), client, script, checksum); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if got := host.files[scriptPath]; got != string(script) {
		t.Errorf("script on host: got %q, want %q", got, script)
	}
	if got := host.files[checksumPath]; got != checksum {
		t.Errorf("checksum on host: got %q, want %q", got, checksum)
	}

	// No exit code yet, so the run is still in flight.
	state, err := Status(t.Context(), client, checksum)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if state.Phase != PhaseRunning {
		t.Errorf("phase: got %s, want %s", state.Phase, PhaseRunning)
	}

	host.files[exitCodePath] = "0"
	state, err = Status(t.Context(), client, checksum)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if state.Phase != PhaseSucceeded {
		t.Errorf("phase: got %s, want %s", state.Phase, PhaseSucceeded)
	}
}

func TestStatusReportsFailureWithTheLogTail(t *testing.T) {
	host := newFakeHost()
	host.files[checksumPath] = "abc123"
	host.files[exitCodePath] = "1"
	host.files[LogPath] = "kubeadm: preflight check failed\n"

	client := connect(t, host)

	state, err := Status(t.Context(), client, "abc123")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if state.Phase != PhaseFailed {
		t.Fatalf("phase: got %s, want %s", state.Phase, PhaseFailed)
	}
	if state.ExitCode != 1 {
		t.Errorf("exit code: got %d, want 1", state.ExitCode)
	}
	if !strings.Contains(state.LogTail, "preflight check failed") {
		t.Errorf("log tail is missing the failure: %q", state.LogTail)
	}
}

// A finished run for different bootstrap data must not be mistaken for this one.
func TestStatusIsIdleWhenTheChecksumDiffers(t *testing.T) {
	host := newFakeHost()
	host.files[checksumPath] = "older"
	host.files[exitCodePath] = "0"

	client := connect(t, host)

	state, err := Status(t.Context(), client, "newer")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if state.Phase != PhaseIdle {
		t.Errorf("phase: got %s, want %s", state.Phase, PhaseIdle)
	}
}

func TestStartRefusesANonRootUser(t *testing.T) {
	host := newFakeHost()
	host.uid = "1000"

	client := connect(t, host)

	err := Start(t.Context(), client, []byte("true"), "abc")
	if err == nil {
		t.Fatal("expected Start to refuse a non-root connection")
	}
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("error should mention root, got %q", err)
	}
}

func TestStartReportsMissingCommands(t *testing.T) {
	host := newFakeHost()
	host.missing["setsid"] = true
	host.missing["base64"] = true

	client := connect(t, host)

	err := Start(t.Context(), client, []byte("true"), "abc")
	if err == nil {
		t.Fatal("expected Start to refuse a host missing required commands")
	}
	for _, want := range []string{"base64", "setsid"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got %q", want, err)
		}
	}
}
