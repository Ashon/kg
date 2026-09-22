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

func TestInjectProviderIDEmitsOneSeparatorPerDocument(t *testing.T) {
	const content = `---
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
clusterName: lab
---
apiVersion: kubeadm.k8s.io/v1beta4
kind: InitConfiguration
nodeRegistration: {}
`

	out, changed, err := injectProviderID([]byte(content), "kgenesis://default/cp-1")
	if err != nil {
		t.Fatalf("injectProviderID: %v", err)
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
