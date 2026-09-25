// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package assets embeds the kgenesis provider manifests in the CLI, so
// `kg init` can install the provider without reaching a manifest registry.
// Regenerate with `make components` after changing anything under config/.
package assets

import _ "embed"

// ProviderComponents is the CRDs, RBAC and controller Deployment that make up
// the kgenesis infrastructure provider.
//
//go:embed provider-components.yaml
var ProviderComponents []byte
