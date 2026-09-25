// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package registry preserves cluster identity and management location after the
// temporary genesis node is gone. Records contain paths, never credentials.
package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Mode string

const (
	Managed     Mode = "managed"
	SelfManaged Mode = "self-managed"
	Released    Mode = "released"
)

// Record is the last confirmed management arrangement, not a health report.
// A failed connection must never change Mode.
type Record struct {
	Version              int       `json:"version"`
	Name                 string    `json:"name"`
	Namespace            string    `json:"namespace"`
	Mode                 Mode      `json:"mode"`
	Endpoint             string    `json:"endpoint"`
	ManagementKubeconfig string    `json:"managementKubeconfig,omitempty"`
	WorkloadKubeconfig   string    `json:"workloadKubeconfig"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

func Key(namespace, name string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + name))
	return hex.EncodeToString(sum[:])
}

func (r Record) Validate() error {
	if r.Version != 1 {
		return fmt.Errorf("unsupported registry version %d", r.Version)
	}
	if r.Name == "" || r.Namespace == "" {
		return errors.New("cluster name and namespace are required")
	}
	switch r.Mode {
	case Managed, SelfManaged:
		if !filepath.IsAbs(r.ManagementKubeconfig) {
			return errors.New("management kubeconfig must be an absolute path")
		}
	case Released:
		if r.ManagementKubeconfig != "" {
			return errors.New("released clusters have no management kubeconfig")
		}
	default:
		return fmt.Errorf("unknown management mode %q", r.Mode)
	}
	if !filepath.IsAbs(r.WorkloadKubeconfig) {
		return errors.New("workload kubeconfig must be an absolute path")
	}
	return nil
}

// Store uses one atomic file per identity so updates to different clusters do
// not overwrite one another. A state directory represents one kg registry.
type Store struct{ Dir string }

func (s Store) path(namespace, name string) string {
	return filepath.Join(s.Dir, "clusters", Key(namespace, name)+".json")
}

func (s Store) Get(namespace, name string) (*Record, error) {
	r, err := read(s.path(namespace, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if r.Name != name || r.Namespace != namespace {
		return nil, errors.New("registry identity does not match its filename")
	}
	return r, nil
}

func read(path string) (*Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("read registry %s: %w", path, err)
	}
	if err := r.Validate(); err != nil {
		return nil, fmt.Errorf("read registry %s: %w", path, err)
	}
	return &r, nil
}

func (s Store) List() ([]Record, error) {
	dir := filepath.Join(s.Dir, "clusters")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		r, err := read(path)
		if err != nil {
			return nil, err
		}
		if filepath.Base(path) != Key(r.Namespace, r.Name)+".json" {
			return nil, fmt.Errorf("registry identity does not match %s", path)
		}
		records = append(records, *r)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Namespace != records[j].Namespace {
			return records[i].Namespace < records[j].Namespace
		}
		return records[i].Name < records[j].Name
	})
	return records, nil
}

func (s Store) Put(r Record) error {
	r.Version = 1
	r.UpdatedAt = time.Now().UTC()
	if err := r.Validate(); err != nil {
		return err
	}
	path := s.path(r.Namespace, r.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".record-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Forget removes only local metadata, never the cluster or its kubeconfig.
func (s Store) Forget(namespace, name string) error {
	err := os.Remove(s.path(namespace, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
