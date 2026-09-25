// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package registry

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func record(dir, namespace, name string, mode Mode) Record {
	workload := filepath.Join(dir, Key(namespace, name)+".kubeconfig")
	management := filepath.Join(dir, "bootstrap.kubeconfig")
	if mode == SelfManaged {
		management = workload
	}
	if mode == Released {
		management = ""
	}
	return Record{Version: 1, Namespace: namespace, Name: name, Mode: mode, WorkloadKubeconfig: workload, ManagementKubeconfig: management}
}

func TestRecordsSurviveGenesisRemovalAndModeChanges(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: dir}
	if err := os.WriteFile(filepath.Join(dir, "bootstrap.kubeconfig"), []byte("bootstrap"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{Managed, SelfManaged, Released} {
		if err := s.Put(record(dir, "lab", "lab", mode)); err != nil {
			t.Fatal(err)
		}
		got, err := (Store{Dir: dir}).Get("lab", "lab")
		if err != nil || got.Mode != mode || got.UpdatedAt.IsZero() {
			t.Fatalf("record = %+v, err = %v", got, err)
		}
	}
	if err := os.Remove(filepath.Join(dir, "bootstrap.kubeconfig")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("lab", "lab")
	if err != nil || got.Mode != Released || got.ManagementKubeconfig != "" {
		t.Fatalf("record = %+v, err = %v", got, err)
	}
	info, err := os.Stat(s.path("lab", "lab"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %v", info.Mode())
	}
}

func TestSeparateIdentitiesAndConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: dir}
	var wg sync.WaitGroup
	for _, ns := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Put(record(dir, ns, "lab", Managed)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	records, err := s.List()
	if err != nil || len(records) != 2 {
		t.Fatalf("records = %+v, err = %v", records, err)
	}
	if err := s.Forget("alpha", "lab"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("beta", "lab")
	if err != nil || got == nil {
		t.Fatalf("other namespace was lost: %+v %v", got, err)
	}
}

func TestInvalidRecordsDoNotReplaceConfirmedState(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: dir}
	original := record(dir, "lab", "lab", Managed)
	if err := s.Put(original); err != nil {
		t.Fatal(err)
	}
	bad := original
	bad.Mode = "offline"
	if err := s.Put(bad); err == nil {
		t.Fatal("accepted connectivity as a management mode")
	}
	got, err := s.Get("lab", "lab")
	if err != nil || got.Mode != Managed {
		t.Fatalf("lost confirmed mode: %+v %v", got, err)
	}
	if err := os.WriteFile(s.path("lab", "lab"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err == nil {
		t.Fatal("corrupt registry was silently ignored")
	}
}
