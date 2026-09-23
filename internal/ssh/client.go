// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package ssh is the transport kgenesis uses to reach pre-provisioned hosts:
// probing them, pushing bootstrap data and resetting them on release.
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// HostKeyPolicy mirrors v1alpha1.HostKeyPolicy without importing the API package.
type HostKeyPolicy string

const (
	PolicyStrict   HostKeyPolicy = "Strict"
	PolicyTOFU     HostKeyPolicy = "TOFU"
	PolicyInsecure HostKeyPolicy = "Insecure"
)

// DefaultDialTimeout bounds the TCP connect and SSH handshake.
const DefaultDialTimeout = 15 * time.Second

// Config is everything needed to open one SSH session.
type Config struct {
	Address string
	Port    int32
	User    string

	// PrivateKey is a PEM encoded key. When empty, Password is used.
	PrivateKey []byte
	Passphrase string
	Password   string

	// Policy selects host key verification. Under Strict and TOFU, KnownPublicKey
	// must hold the expected key in authorized_keys format; under TOFU an empty
	// KnownPublicKey means "first contact", and the key seen is reported back in
	// Client.HostKey for the caller to pin.
	Policy         HostKeyPolicy
	KnownPublicKey string

	DialTimeout time.Duration
}

// Client is a connected SSH session factory for one host.
type Client struct {
	client *ssh.Client
	cfg    Config

	// HostKey is the key the server presented, in authorized_keys format. Callers
	// pin it into Host.status.observedPublicKey on first contact under TOFU.
	HostKey string
}

// ErrHostKeyMismatch is returned when the presented key differs from the pinned
// one. It is deliberately distinct: it means the host changed identity, which an
// operator has to resolve rather than the controller retrying forever.
var ErrHostKeyMismatch = errors.New("ssh host key mismatch")

// Dial opens a connection. The context bounds the whole handshake.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}

	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}

	c := &Client{cfg: cfg}

	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: c.hostKeyCallback(),
		Timeout:         cfg.DialTimeout,
	}

	addr := net.JoinHostPort(cfg.Address, strconv.Itoa(int(cfg.Port)))

	dialer := &net.Dialer{Timeout: cfg.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	// The handshake has its own deadline; DialContext only covered the TCP connect.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(cfg.DialTimeout))
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		_ = conn.Close()
		if errors.Is(err, ErrHostKeyMismatch) {
			return nil, err
		}
		return nil, fmt.Errorf("ssh handshake with %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Time{})

	c.client = ssh.NewClient(sshConn, chans, reqs)
	return c, nil
}

func authMethods(cfg Config) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if len(cfg.PrivateKey) > 0 {
		var signer ssh.Signer
		var err error
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(cfg.PrivateKey, []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(cfg.PrivateKey)
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}

	if len(methods) == 0 {
		return nil, errors.New("no ssh credential: neither private key nor password was provided")
	}
	return methods, nil
}

func (c *Client) hostKeyCallback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		presented := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		c.HostKey = presented

		switch c.cfg.Policy {
		case PolicyInsecure:
			return nil

		case PolicyStrict:
			if c.cfg.KnownPublicKey == "" {
				return fmt.Errorf("%w: policy is Strict but no public key is configured for %s",
					ErrHostKeyMismatch, hostname)
			}

		case PolicyTOFU:
			if c.cfg.KnownPublicKey == "" {
				// First contact: accept and let the caller pin c.HostKey.
				return nil
			}

		default:
			return fmt.Errorf("unknown host key policy %q", c.cfg.Policy)
		}

		if !sameAuthorizedKey(c.cfg.KnownPublicKey, presented) {
			return fmt.Errorf("%w for %s: expected %s, got %s",
				ErrHostKeyMismatch, hostname, abbreviate(c.cfg.KnownPublicKey), abbreviate(presented))
		}
		return nil
	}
}

// sameAuthorizedKey compares two authorized_keys lines by their parsed key
// material, so a differing trailing comment does not read as a mismatch.
func sameAuthorizedKey(a, b string) bool {
	ka, _, _, _, errA := ssh.ParseAuthorizedKey([]byte(a))
	kb, _, _, _, errB := ssh.ParseAuthorizedKey([]byte(b))
	if errA != nil || errB != nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return bytes.Equal(ka.Marshal(), kb.Marshal())
}

func abbreviate(authorizedKey string) string {
	fields := strings.Fields(authorizedKey)
	if len(fields) < 2 {
		return authorizedKey
	}
	blob := fields[1]
	if len(blob) <= 20 {
		return fields[0] + " " + blob
	}
	return fields[0] + " " + blob[:10] + "..." + blob[len(blob)-10:]
}

// Result is the outcome of one remote command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExitError reports a command that ran to completion with a non-zero status.
type ExitError struct {
	Command string
	Result  Result
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Result.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(e.Result.Stdout)
	}
	if msg == "" {
		return fmt.Sprintf("command %q exited %d", e.Command, e.Result.ExitCode)
	}
	return fmt.Sprintf("command %q exited %d: %s", e.Command, e.Result.ExitCode, msg)
}

// Run executes cmd and waits for it. A non-zero exit becomes an *ExitError; the
// Result is still returned so callers can log the output either way.
func (c *Client) Run(ctx context.Context, cmd string) (Result, error) {
	return c.run(ctx, cmd, nil)
}

// RunWithInput is Run with the command's stdin fed from r. It streams rather
// than buffering, so an image archive can be piped straight to a host without
// ever being held in memory twice.
func (c *Client) RunWithInput(ctx context.Context, cmd string, r io.Reader) (Result, error) {
	return c.run(ctx, cmd, r)
}

func (c *Client) run(ctx context.Context, cmd string, stdin io.Reader) (Result, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("open session: %w", err)
	}
	defer func() { _ = session.Close() }()

	if stdin != nil {
		session.Stdin = stdin
	}

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case <-ctx.Done():
		// Signalling is best effort; closing the session is what actually unblocks.
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		<-done
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, ctx.Err()
	case runErr := <-done:
		res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
		var exitErr *ssh.ExitError
		switch {
		case runErr == nil:
			return res, nil
		case errors.As(runErr, &exitErr):
			res.ExitCode = exitErr.ExitStatus()
			return res, &ExitError{Command: cmd, Result: res}
		default:
			return res, fmt.Errorf("run %q: %w", cmd, runErr)
		}
	}
}

// WriteFile uploads content to path with the given octal mode, creating parent
// directories. It streams over stdin rather than shelling the content in, so
// binary data and arbitrary quoting are safe.
func (c *Client) WriteFile(ctx context.Context, path string, content []byte, mode string) error {
	session, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	defer func() { _ = session.Close() }()

	session.Stdin = bytes.NewReader(content)
	var stderr bytes.Buffer
	session.Stderr = &stderr

	cmd := writeFileCommand(path, mode)

	done := make(chan error, 1)
	go func() { done <- session.Run(cmd) }()

	select {
	case <-ctx.Done():
		_ = session.Close()
		<-done
		return ctx.Err()
	case runErr := <-done:
		if runErr != nil {
			return fmt.Errorf("write %s: %w: %s", path, runErr, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

// writeFileCommand builds the remote side of WriteFile.
//
// mkdir -p rather than install -d: install sets the mode of a directory that is
// already there, and the parent is sometimes one the system owns. Writing a
// script to /tmp with install -d -m 0755 takes /tmp from 1777 to 0755, which
// breaks every unprivileged process on that machine that needs a temporary
// file - apt among them, which then reports every repository as unsigned and
// says nothing about why.
func writeFileCommand(path, mode string) string {
	return fmt.Sprintf("mkdir -p %s && cat > %s && chmod %s %s",
		shellQuote(dirOf(path)), shellQuote(path), shellQuote(mode), shellQuote(path))
}

// Close releases the connection.
func (c *Client) Close() error {
	if c.client == nil {
		return nil
	}
	return c.client.Close()
}

func dirOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "/"
	}
	return path[:i]
}

// shellQuote wraps s in single quotes for POSIX shells.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
