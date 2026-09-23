// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cloudinit

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// CABPK opens its kubeadm configuration with a separator, and the YAML reader
// hands that separator back attached to the first document. Emitting it again
// produced a leading empty document, which kubeadm rejects with "kind and
// apiVersion is mandatory" - a message that says nothing about the real cause.
func TestSplitYAMLDropsTheOpeningSeparator(t *testing.T) {
	const content = `---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
clusterName: lab
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
nodeRegistration: {}
`

	docs, err := splitYAML([]byte(content))
	if err != nil {
		t.Fatalf("splitYAML: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d: %q", len(docs), docs)
	}
	for i, doc := range docs {
		if strings.HasPrefix(strings.TrimSpace(string(doc)), "---") {
			t.Errorf("document %d still carries a separator: %q", i, doc)
		}
	}
}

func TestPatchKubeadmConfigEmitsOneSeparatorPerDocument(t *testing.T) {
	const content = `---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
clusterName: lab
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
nodeRegistration: {}
`

	out, changed, err := patchKubeadmConfig([]byte(content), kubeadmPatch{
		kubeletArgs: []kubeletArg{
			{providerIDFlag, "kgenesis://default/cp-1"},
			{nodeIPFlag, "192.168.105.11"},
		},
	})
	if err != nil {
		t.Fatalf("patchKubeadmConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected the InitConfiguration to be patched")
	}

	if strings.HasPrefix(string(out), "---\n---") {
		t.Errorf("an empty leading document was emitted:\n%s", out)
	}

	docs, err := splitYAML(out)
	if err != nil {
		t.Fatalf("re-split: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents after the rewrite, got %d:\n%s", len(docs), out)
	}

	// Every document kubeadm reads has to carry its kind and apiVersion.
	for i, doc := range docs {
		var parsed map[string]any
		if err := yaml.Unmarshal(doc, &parsed); err != nil {
			t.Fatalf("document %d does not parse: %v", i, err)
		}
		if parsed["kind"] == nil || parsed["apiVersion"] == nil {
			t.Errorf("document %d is missing kind or apiVersion: %v", i, parsed)
		}
	}
}

func TestStripLeadingSeparator(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"separator then content": {"---\nkind: A\n", "kind: A\n"},
		"comment after it":       {"--- # a note\nkind: A\n", "kind: A\n"},
		"no separator":           {"kind: A\n", "kind: A\n"},
		"separator only":         {"---\n", ""},
		// Content, not a separator: a document may legitimately start this way.
		"dashes with content": {"---foo\nkind: A\n", "---foo\nkind: A\n"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := string(stripLeadingSeparator([]byte(tc.in))); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A host with more than one interface, or one behind a per-machine NAT, lets the
// kubelet advertise an address the rest of the cluster cannot use - and when the
// NAT hands every host the same address, every Node reports it. Both kubeadm
// shapes have to carry the pinned one.
func TestPatchKubeadmConfigPinsNodeIP(t *testing.T) {
	cases := map[string]struct {
		apiVersion string
		want       string
	}{
		"v1beta4 list": {"kubeadm.k8s.io/v1beta4", "- name: node-ip\n    value: 192.168.105.11"},
		"v1beta3 map":  {"kubeadm.k8s.io/v1beta3", "node-ip: 192.168.105.11"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			content := "apiVersion: " + tc.apiVersion + "\nkind: JoinConfiguration\nnodeRegistration: {}\n"

			out, changed, err := patchKubeadmConfig([]byte(content), kubeadmPatch{
				kubeletArgs: []kubeletArg{
					{providerIDFlag, "kgenesis://lab/cp-1"},
					{nodeIPFlag, "192.168.105.11"},
				},
			})
			if err != nil {
				t.Fatalf("patchKubeadmConfig: %v", err)
			}
			if !changed {
				t.Fatal("expected the JoinConfiguration to be patched")
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("node-ip is missing from the rendered configuration:\n%s", out)
			}
			if !strings.Contains(string(out), "kgenesis://lab/cp-1") {
				t.Errorf("provider-id was lost while adding node-ip:\n%s", out)
			}
		})
	}
}

// The first control plane node decides what the kubernetes Service points at,
// and every later one decides where its etcd member is reachable. Neither can be
// left to the default route.
func TestPatchKubeadmConfigPinsAdvertiseAddress(t *testing.T) {
	cases := map[string]struct {
		content string
		want    string
	}{
		"init configuration": {
			"apiVersion: kubeadm.k8s.io/v1beta4\nkind: InitConfiguration\nnodeRegistration: {}\n",
			"localAPIEndpoint:\n  advertiseAddress: 192.168.105.11",
		},
		"control plane join": {
			"apiVersion: kubeadm.k8s.io/v1beta4\nkind: JoinConfiguration\ncontrolPlane: {}\nnodeRegistration: {}\n",
			"advertiseAddress: 192.168.105.11",
		},
		// A worker has no endpoint of its own, so nothing is added.
		"worker join": {
			"apiVersion: kubeadm.k8s.io/v1beta4\nkind: JoinConfiguration\nnodeRegistration: {}\n",
			"",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, _, err := patchKubeadmConfig([]byte(tc.content), kubeadmPatch{
				advertiseAddress: "192.168.105.11",
			})
			if err != nil {
				t.Fatalf("patchKubeadmConfig: %v", err)
			}

			if tc.want == "" {
				if strings.Contains(string(out), "advertiseAddress") {
					t.Errorf("a worker join was given an advertise address:\n%s", out)
				}
				return
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("expected %q in:\n%s", tc.want, out)
			}
		})
	}
}
