// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Command kgenesis is the genesis node CLI: it bootstraps a Cluster API
// management cluster locally, stamps a Kubernetes cluster onto pre-provisioned
// hosts over SSH, and then hands management to that cluster.
package main

import "github.com/Ashon/kgenesis/internal/cli"

func main() {
	cli.Execute()
}
