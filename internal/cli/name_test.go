// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestResolveBinaryName(t *testing.T) {
	original := os.Args
	t.Cleanup(func() { os.Args = original })

	cases := map[string]struct{ argv0, want string }{
		"canonical":     {"kg", CanonicalName},
		"absolute path": {"/usr/local/bin/kg", CanonicalName},
		// A binary someone renamed is still the name they type.
		"renamed":         {"kg-lab", "kg-lab"},
		"go build output": {"/tmp/go-build123/b001/exe/kg", CanonicalName},
		"windows suffix":  {"kg.exe", CanonicalName},
		// A test binary's name is not something an operator would ever type.
		"test binary": {"/tmp/cli.test", CanonicalName},
		"empty argv0": {"", CanonicalName},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			os.Args = []string{tc.argv0}
			if got := resolveBinaryName(); got != tc.want {
				t.Errorf("resolveBinaryName() for %q: got %q, want %q", tc.argv0, got, tc.want)
			}
		})
	}
}

func TestPadLinesUpTheHintColumn(t *testing.T) {
	short := pad(CanonicalName + " init")
	long := pad(CanonicalName + " cluster create")

	if len(short) != len(long) {
		t.Errorf("hint column does not line up: %q is %d wide, %q is %d",
			short, len(short), long, len(long))
	}

	// An invocation past the column still gets separated from its description.
	overflow := pad(strings.Repeat("x", hintColumn+5))
	if !strings.HasSuffix(overflow, " ") {
		t.Errorf("an overlong invocation was not separated from its description: %q", overflow)
	}
}

// Every invocation the CLI suggests has to be built from the name the operator
// typed, so a renamed binary still suggests commands that exist.
func TestNoHardcodedInvocationsInHints(t *testing.T) {
	// Subcommands as they appear at the top level.
	subcommands := []string{
		"config", "inventory", "init", "cluster", "cni", "kubeconfig", "pivot", "reset", "version",
	}
	hardcoded := regexp.MustCompile(`kgenesis (` + strings.Join(subcommands, "|") + `)\b`)

	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		for i, line := range strings.Split(string(source), "\n") {
			match := hardcoded.FindString(line)
			if match == "" {
				continue
			}
			t.Errorf(`%s:%d hardcodes %q; build it with invoke(%q) instead:
	%s`, path, i+1, match, strings.TrimPrefix(match, "kgenesis "), strings.TrimSpace(line))
		}
	}
}

func TestDefaultConfigPath(t *testing.T) {
	original, hadEnv := os.LookupEnv("KG_CONFIG")
	t.Cleanup(func() {
		if hadEnv {
			os.Setenv("KG_CONFIG", original)
		} else {
			os.Unsetenv("KG_CONFIG")
		}
	})

	// KG_CONFIG selects a fleet for a shell without repeating --config.
	os.Setenv("KG_CONFIG", "/somewhere/lab.yaml")
	if got := defaultConfigPath(); got != "/somewhere/lab.yaml" {
		t.Errorf("with KG_CONFIG set: got %q", got)
	}

	os.Unsetenv("KG_CONFIG")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	want := filepath.Join(home, ConfigDir, ConfigFile)
	if got := defaultConfigPath(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// The path must not depend on where the command was run from.
	dir := t.TempDir()
	t.Chdir(dir)
	if got := defaultConfigPath(); got != want {
		t.Errorf("after changing directory: got %q, want %q", got, want)
	}
}
