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

// kubeletArg is one flag to pin under nodeRegistration.kubeletExtraArgs. They
// are applied in order, so the rendered configuration is stable.
type kubeletArg struct {
	name  string
	value string
}

// kubeadmPatch is everything kgenesis knows about the machine that the bootstrap
// data, written before any host was claimed, could not.
type kubeadmPatch struct {
	kubeletArgs      []kubeletArg
	advertiseAddress string
}

// empty reports whether there is nothing to write.
func (p kubeadmPatch) empty() bool {
	return len(p.kubeletArgs) == 0 && p.advertiseAddress == ""
}

// patchKubeadmConfigs writes the patch into whichever kubeadm configuration the
// bootstrap data carries.
//
// Nothing is skipped silently: if no kubeadm configuration is found, the machine
// would join without a provider ID, Cluster API would never pair its Node with
// its Machine, and the cluster would look healthy while remaining unmanageable.
// That is worth failing the bootstrap over.
func patchKubeadmConfigs(files []*resolvedFile, patch kubeadmPatch) error {
	patched := 0

	for _, f := range files {
		content, changed, err := patchKubeadmConfig(f.content, patch)
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

// patchKubeadmConfig rewrites every kubeadm configuration document in content,
// and reports whether it changed anything.
func patchKubeadmConfig(content []byte, patch kubeadmPatch) ([]byte, bool, error) {
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

		for _, arg := range patch.kubeletArgs {
			if err := setKubeletExtraArg(doc, apiVersion, arg.name, arg.value); err != nil {
				return nil, false, fmt.Errorf("%s: %w", kind, err)
			}
		}
		if patch.advertiseAddress != "" {
			if err := setAdvertiseAddress(doc, kind, patch.advertiseAddress); err != nil {
				return nil, false, fmt.Errorf("%s: %w", kind, err)
			}
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

// setAdvertiseAddress pins the address a control plane node publishes for its
// API server, and with it the client and peer URLs etcd derives from it.
//
// kubeadm defaults it to the address of the default route, which is the same
// mistake --node-ip corrects for the kubelet. On a multi-homed host it picks the
// wrong interface; behind a per-machine NAT every control plane node ends up
// publishing an identical address, so the kubernetes Service points at whichever
// answers and the second etcd member never reaches the first.
func setAdvertiseAddress(doc map[string]any, kind, address string) error {
	switch kind {
	case "InitConfiguration":
		return setNested(doc, address, "localAPIEndpoint", "advertiseAddress")
	case "JoinConfiguration":
		// Only a control plane join brings an endpoint of its own; a worker has
		// nothing to advertise.
		if _, ok := doc["controlPlane"]; !ok {
			return nil
		}
		return setNested(doc, address, "controlPlane", "localAPIEndpoint", "advertiseAddress")
	}
	return nil
}

// setNested writes value at path, creating the mappings along the way.
func setNested(doc map[string]any, value string, path ...string) error {
	current := doc
	for i, key := range path[:len(path)-1] {
		next, present := current[key]
		if !present || next == nil {
			created := map[string]any{}
			current[key] = created
			current = created
			continue
		}
		m, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is not a mapping", strings.Join(path[:i+1], "."))
		}
		current = m
	}
	current[path[len(path)-1]] = value
	return nil
}
