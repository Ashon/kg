// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cloudinit

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// setProviderID writes the provider ID into whichever kubeadm configuration the
// bootstrap data carries.
//
// Nothing is skipped silently: if no kubeadm configuration is found, the machine
// would join without a provider ID, Cluster API would never pair its Node with
// its Machine, and the cluster would look healthy while remaining unmanageable.
// That is worth failing the bootstrap over.
func setProviderID(files []*resolvedFile, providerID string) error {
	patched := 0

	for _, f := range files {
		content, changed, err := injectProviderID(f.content, providerID)
		if err != nil {
			return fmt.Errorf("write_files %s: %w", f.Path, err)
		}
		if !changed {
			continue
		}
		f.content = content
		// Whatever the entry arrived as, it is plain YAML now.
		f.Encoding = ""
		patched++
	}

	if patched == 0 {
		return errors.New("bootstrap data contains no kubeadm InitConfiguration or " +
			"JoinConfiguration, so the kubelet provider ID cannot be set and Cluster API " +
			"would not be able to match this machine to its Node")
	}
	return nil
}

// injectProviderID sets nodeRegistration.kubeletExtraArgs on every kubeadm
// configuration document in content, and reports whether it changed anything.
func injectProviderID(content []byte, providerID string) ([]byte, bool, error) {
	documents, err := splitYAML(content)
	if err != nil {
		// Not every write_files entry is YAML; those simply have nothing to patch.
		return nil, false, nil //nolint:nilerr // a parse failure means "not a kubeadm config"
	}

	changed := false
	rendered := make([][]byte, 0, len(documents))

	for _, raw := range documents {
		doc := map[string]any{}
		if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc) == 0 {
			rendered = append(rendered, raw)
			continue
		}

		apiVersion, _ := doc["apiVersion"].(string)
		kind, _ := doc["kind"].(string)
		if !strings.HasPrefix(apiVersion, kubeadmAPIGroupPrefix) || !kubeadmConfigKinds[kind] {
			rendered = append(rendered, raw)
			continue
		}

		if err := setKubeletExtraArg(doc, apiVersion, providerIDFlag, providerID); err != nil {
			return nil, false, fmt.Errorf("%s: %w", kind, err)
		}

		out, err := yaml.Marshal(doc)
		if err != nil {
			return nil, false, fmt.Errorf("re-encode %s: %w", kind, err)
		}
		rendered = append(rendered, out)
		changed = true
	}

	if !changed {
		return content, false, nil
	}
	return joinYAML(rendered), true, nil
}

// setKubeletExtraArg sets one kubelet flag under nodeRegistration.
//
// The field changed shape in kubeadm v1beta4: it was a map of flag to value, and
// became a list of {name, value} so that a flag can be repeated. Both are still
// in use, because CABPK emits v1beta3 for Kubernetes below 1.31.
func setKubeletExtraArg(doc map[string]any, apiVersion, name, value string) error {
	registration, ok := doc["nodeRegistration"].(map[string]any)
	if !ok {
		// Absent, or present but null, which is how an empty section round-trips.
		registration = map[string]any{}
		doc["nodeRegistration"] = registration
	}

	switch existing := registration["kubeletExtraArgs"].(type) {
	case []any:
		registration["kubeletExtraArgs"] = upsertArgList(existing, name, value)

	case map[string]any:
		existing[name] = value

	case nil:
		if usesArgList(apiVersion) {
			registration["kubeletExtraArgs"] = upsertArgList(nil, name, value)
		} else {
			registration["kubeletExtraArgs"] = map[string]any{name: value}
		}

	default:
		return fmt.Errorf("nodeRegistration.kubeletExtraArgs has unexpected type %T", existing)
	}
	return nil
}

// usesArgList reports whether a kubeadm API version expects the list form.
// v1beta1 through v1beta3 use a map; v1beta4 introduced the list, and anything
// newer is assumed to keep it.
func usesArgList(apiVersion string) bool {
	version := strings.TrimPrefix(apiVersion, kubeadmAPIGroupPrefix)
	switch version {
	case "v1beta1", "v1beta2", "v1beta3":
		return false
	default:
		return true
	}
}

func upsertArgList(args []any, name, value string) []any {
	for _, arg := range args {
		entry, ok := arg.(map[string]any)
		if ok && entry["name"] == name {
			entry["value"] = value
			return args
		}
	}
	return append(args, map[string]any{"name": name, "value": value})
}

// splitYAML separates a multi-document stream into its documents.
func splitYAML(content []byte) ([][]byte, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(content)))

	var documents [][]byte
	for {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return documents, nil
		}
		if err != nil {
			return nil, err
		}

		// The reader hands back the opening separator attached to the first
		// document. Stripping it here keeps every document to its own content,
		// and leaves joinYAML as the only thing that writes separators.
		doc = stripLeadingSeparator(doc)
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		documents = append(documents, doc)
	}
}

// stripLeadingSeparator removes one leading "---" line, if there is one.
func stripLeadingSeparator(doc []byte) []byte {
	trimmed := bytes.TrimLeft(doc, " \t\n\r")
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return doc
	}

	after := trimmed[len("---"):]

	end := bytes.IndexByte(after, '\n')
	if end < 0 {
		// The document is nothing but a separator.
		if len(bytes.TrimSpace(after)) == 0 {
			return nil
		}
		return doc
	}

	// Only whitespace or a comment may follow, otherwise this is content that
	// merely starts with three dashes.
	rest := bytes.TrimSpace(after[:end])
	if len(rest) != 0 && rest[0] != '#' {
		return doc
	}
	return after[end+1:]
}

func joinYAML(documents [][]byte) []byte {
	var b bytes.Buffer
	for _, doc := range documents {
		trimmed := bytes.TrimSpace(doc)
		if len(trimmed) == 0 {
			continue
		}
		b.WriteString("---\n")
		b.Write(trimmed)
		b.WriteByte('\n')
	}
	return b.Bytes()
}
