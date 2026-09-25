// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package controller implements the kg infrastructure provider: it claims
// pre-provisioned Hosts for Cluster API Machines and bootstraps them over SSH.
package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
	"github.com/Ashon/kg/internal/ssh"
)

// Secret keys recognised in a Host's sshSecretRef.
const (
	SecretKeyPrivateKey = "private_key"
	SecretKeyPassphrase = "passphrase"
	SecretKeyPassword   = "password"
)

// sshConfigFor assembles the SSH parameters for a Host, resolving its credential
// Secret and applying the host key policy.
func sshConfigFor(ctx context.Context, c client.Client, host *infrav1.Host) (ssh.Config, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: host.Namespace, Name: host.Spec.SSHSecretRef.Name}
	if err := c.Get(ctx, key, secret); err != nil {
		return ssh.Config{}, fmt.Errorf("get ssh secret %s: %w", key, err)
	}

	cfg := ssh.Config{
		Address:    host.Spec.Address,
		Port:       host.Spec.Port,
		User:       host.Spec.User,
		PrivateKey: secret.Data[SecretKeyPrivateKey],
		Passphrase: string(secret.Data[SecretKeyPassphrase]),
		Password:   string(secret.Data[SecretKeyPassword]),
		Policy:     ssh.HostKeyPolicy(host.Spec.HostKeyPolicy),
	}

	if len(cfg.PrivateKey) == 0 && cfg.Password == "" {
		return ssh.Config{}, fmt.Errorf("secret %s has neither %q nor %q",
			key, SecretKeyPrivateKey, SecretKeyPassword)
	}

	switch cfg.Policy {
	case ssh.PolicyStrict:
		cfg.KnownPublicKey = host.Spec.PublicKey
	case ssh.PolicyTOFU:
		// Empty on first contact; the caller pins what the host presents.
		cfg.KnownPublicKey = host.Status.ObservedPublicKey
	}

	return cfg, nil
}

// connect opens a session to a Host and pins the presented key when the TOFU
// policy is in effect and nothing has been pinned yet. The caller is responsible
// for persisting host.Status.
func connect(ctx context.Context, c client.Client, host *infrav1.Host) (*ssh.Client, error) {
	cfg, err := sshConfigFor(ctx, c, host)
	if err != nil {
		return nil, err
	}

	conn, err := ssh.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}

	if host.Spec.HostKeyPolicy == infrav1.HostKeyPolicyTOFU && host.Status.ObservedPublicKey == "" {
		host.Status.ObservedPublicKey = conn.HostKey
	}
	return conn, nil
}
