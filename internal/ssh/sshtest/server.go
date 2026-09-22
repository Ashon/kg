// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package sshtest provides an in-process SSH server for tests, so code that
// talks to hosts can be exercised without a real machine or a container.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	kgssh "github.com/Ashon/kgenesis/internal/ssh"
)

// Handler runs one remote command. stdin carries whatever the client streamed.
type Handler func(cmd string, stdin io.Reader) (stdout string, stderr string, exit int)

// Server is a running SSH server bound to a loopback port.
type Server struct {
	listener net.Listener
	config   *ssh.ServerConfig
	hostKey  ssh.Signer

	mu       sync.Mutex
	commands []string
	handler  Handler
}

// NewServer starts a server that accepts clientPub and closes with the test.
func NewServer(t *testing.T, clientPub ssh.PublicKey, handler Handler) *Server {
	t.Helper()

	if handler == nil {
		handler = func(string, io.Reader) (string, string, int) { return "", "", 0 }
	}

	s := &Server{hostKey: GenerateSigner(t), handler: handler}
	s.config = &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if clientPub != nil && string(key.Marshal()) == string(clientPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unknown public key")
		},
	}
	s.config.AddHostKey(s.hostKey)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.listener = listener

	go s.serve()
	t.Cleanup(func() { _ = listener.Close() })

	return s
}

// Addr is the host and port the server is listening on.
func (s *Server) Addr() (string, int32) {
	host, port, _ := net.SplitHostPort(s.listener.Addr().String())
	n, _ := strconv.Atoi(port)
	return host, int32(n)
}

// HostKeyAuthorized is the server's host key in authorized_keys format.
func (s *Server) HostKeyAuthorized() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.hostKey.PublicKey())))
}

// SetHandler replaces the command handler.
func (s *Server) SetHandler(h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

// Commands returns every command the server has been asked to run.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *Server) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()
	go ssh.DiscardRequests(reqs)

	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only sessions are supported")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go s.handleSession(channel, requests)
	}
}

func (s *Server) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()

	for req := range requests {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}

		// An exec request payload is a length-prefixed command string.
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			continue
		}
		_ = req.Reply(true, nil)

		s.mu.Lock()
		s.commands = append(s.commands, payload.Command)
		handler := s.handler
		s.mu.Unlock()

		stdout, stderr, exit := handler(payload.Command, channel)
		_, _ = io.WriteString(channel, stdout)
		_, _ = io.WriteString(channel.Stderr(), stderr)
		_, _ = channel.SendRequest("exit-status", false,
			ssh.Marshal(struct{ Status uint32 }{uint32(exit)}))
		return
	}
}

// GenerateSigner returns a fresh ed25519 signer.
func GenerateSigner(t *testing.T) ssh.Signer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return signer
}

// GenerateClientKey returns a PEM encoded private key and its signer.
func GenerateClientKey(t *testing.T) ([]byte, ssh.Signer) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return pem.EncodeToMemory(block), signer
}

// Connect starts a server with the given handler and returns a connected client.
// It is the one-liner most tests want.
func Connect(t *testing.T, handler Handler) *kgssh.Client {
	t.Helper()

	key, signer := GenerateClientKey(t)
	server := NewServer(t, signer.PublicKey(), handler)
	host, port := server.Addr()

	client, err := kgssh.Dial(t.Context(), kgssh.Config{
		Address: host, Port: port, User: "root",
		PrivateKey: key, Policy: kgssh.PolicyInsecure,
	})
	if err != nil {
		t.Fatalf("dial the test server: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}
