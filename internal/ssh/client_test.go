// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package ssh_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	cryptossh "golang.org/x/crypto/ssh"

	"github.com/Ashon/kg/internal/ssh"
	"github.com/Ashon/kg/internal/ssh/sshtest"
)

func TestDialAndRun(t *testing.T) {
	key, signer := sshtest.GenerateClientKey(t)
	server := sshtest.NewServer(t, signer.PublicKey(), nil)
	server.SetHandler(func(cmd string, _ io.Reader) (string, string, int) {
		if cmd == "id -u" {
			return "0\n", "", 0
		}
		return "", "no such command", 127
	})

	host, port := server.Addr()
	client, err := ssh.Dial(t.Context(), ssh.Config{
		Address: host, Port: port, User: "root",
		PrivateKey: key, Policy: ssh.PolicyInsecure,
	})
	if err != nil {
		t.Fatalf("ssh.Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	res, err := client.Run(t.Context(), "id -u")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "0" {
		t.Errorf("stdout: got %q, want %q", res.Stdout, "0")
	}

	// A non-zero exit has to surface as an error while still returning the output.
	res, err = client.Run(t.Context(), "nope")
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected an *ssh.ExitError, got %v", err)
	}
	if exitErr.Result.ExitCode != 127 {
		t.Errorf("exit code: got %d, want 127", exitErr.Result.ExitCode)
	}
	if !strings.Contains(exitErr.Error(), "no such command") {
		t.Errorf("error should carry stderr, got %q", exitErr.Error())
	}
}

func TestWriteFileStreamsContent(t *testing.T) {
	key, signer := sshtest.GenerateClientKey(t)
	server := sshtest.NewServer(t, signer.PublicKey(), nil)

	var received string
	server.SetHandler(func(_ string, stdin io.Reader) (string, string, int) {
		data, _ := io.ReadAll(stdin)
		received = string(data)
		return "", "", 0
	})

	host, port := server.Addr()
	client, err := ssh.Dial(t.Context(), ssh.Config{
		Address: host, Port: port, User: "root",
		PrivateKey: key, Policy: ssh.PolicyInsecure,
	})
	if err != nil {
		t.Fatalf("ssh.Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Content that would break any approach that interpolated it into the command.
	content := []byte("line 'one'\n$(rm -rf /)\n\x00binary\n")
	if err := client.WriteFile(t.Context(), "/var/lib/kgenesis/bootstrap.sh", content, "0700"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if received != string(content) {
		t.Errorf("content: got %q, want %q", received, content)
	}

	cmds := server.Commands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %v", cmds)
	}
	for _, want := range []string{
		"mkdir -p '/var/lib/kgenesis'",
		"cat > '/var/lib/kgenesis/bootstrap.sh'",
		"chmod '0700' '/var/lib/kgenesis/bootstrap.sh'",
	} {
		if !strings.Contains(cmds[0], want) {
			t.Errorf("command %q is missing %q", cmds[0], want)
		}
	}
}

func TestHostKeyPolicies(t *testing.T) {
	key, signer := sshtest.GenerateClientKey(t)
	server := sshtest.NewServer(t, signer.PublicKey(), nil)
	host, port := server.Addr()

	base := ssh.Config{Address: host, Port: port, User: "root", PrivateKey: key}

	t.Run("TOFU pins the key on first contact", func(t *testing.T) {
		cfg := base
		cfg.Policy = ssh.PolicyTOFU

		client, err := ssh.Dial(t.Context(), cfg)
		if err != nil {
			t.Fatalf("ssh.Dial: %v", err)
		}
		defer func() { _ = client.Close() }()

		if client.HostKey != server.HostKeyAuthorized() {
			t.Errorf("HostKey: got %q, want %q", client.HostKey, server.HostKeyAuthorized())
		}
	})

	t.Run("TOFU accepts the pinned key", func(t *testing.T) {
		cfg := base
		cfg.Policy = ssh.PolicyTOFU
		cfg.KnownPublicKey = server.HostKeyAuthorized()

		client, err := ssh.Dial(t.Context(), cfg)
		if err != nil {
			t.Fatalf("ssh.Dial: %v", err)
		}
		_ = client.Close()
	})

	t.Run("TOFU rejects a different key", func(t *testing.T) {
		cfg := base
		cfg.Policy = ssh.PolicyTOFU
		cfg.KnownPublicKey = strings.TrimSpace(
			string(cryptossh.MarshalAuthorizedKey(sshtest.GenerateSigner(t).PublicKey())))

		_, err := ssh.Dial(t.Context(), cfg)
		if !errors.Is(err, ssh.ErrHostKeyMismatch) {
			t.Fatalf("expected ssh.ErrHostKeyMismatch, got %v", err)
		}
	})

	t.Run("Strict without a configured key fails", func(t *testing.T) {
		cfg := base
		cfg.Policy = ssh.PolicyStrict

		_, err := ssh.Dial(t.Context(), cfg)
		if !errors.Is(err, ssh.ErrHostKeyMismatch) {
			t.Fatalf("expected ssh.ErrHostKeyMismatch, got %v", err)
		}
	})

	t.Run("a trailing comment is not a mismatch", func(t *testing.T) {
		cfg := base
		cfg.Policy = ssh.PolicyStrict
		cfg.KnownPublicKey = server.HostKeyAuthorized() + " operator@genesis"

		client, err := ssh.Dial(t.Context(), cfg)
		if err != nil {
			t.Fatalf("ssh.Dial: %v", err)
		}
		_ = client.Close()
	})
}

func TestDialTimesOutOnADeadAddress(t *testing.T) {
	key, _ := sshtest.GenerateClientKey(t)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	// 192.0.2.0/24 is reserved for documentation and is never routable.
	_, err := ssh.Dial(ctx, ssh.Config{
		Address: "192.0.2.1", Port: 22, User: "root",
		PrivateKey: key, Policy: ssh.PolicyInsecure, DialTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("expected a dial error against an unroutable address")
	}
}
