// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package provisioner

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Ashon/kgenesis/internal/ssh"
)

// SystemInfo is what a probe learns about a host.
type SystemInfo struct {
	Hostname         string
	OSImage          string
	KernelVersion    string
	Architecture     string
	CPUCores         int32
	MemoryMB         int64
	ContainerRuntime string
}

// probeScript emits key=value lines. Every lookup is guarded so a field that is
// unavailable comes back empty instead of failing the whole probe.
const probeScript = `
echo "hostname=$(hostname 2>/dev/null || echo)"
echo "kernel=$(uname -r 2>/dev/null || echo)"
echo "arch=$(uname -m 2>/dev/null || echo)"
echo "cpu=$(nproc 2>/dev/null || echo)"
echo "memkb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null || echo)"
if [ -r /etc/os-release ]; then
  . /etc/os-release
  echo "os=${PRETTY_NAME:-${NAME:-}}"
else
  echo "os="
fi
runtime=
for candidate in containerd crio dockerd; do
  if command -v "$candidate" >/dev/null 2>&1; then
    runtime="$candidate $("$candidate" --version 2>/dev/null | head -1)"
    break
  fi
done
echo "runtime=${runtime}"
`

// Probe collects host facts over an existing connection.
func Probe(ctx context.Context, c *ssh.Client) (*SystemInfo, error) {
	res, err := c.Run(ctx, probeScript)
	if err != nil {
		return nil, fmt.Errorf("probe host: %w", err)
	}

	fields := map[string]string{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found {
			fields[key] = strings.TrimSpace(value)
		}
	}

	info := &SystemInfo{
		Hostname:         fields["hostname"],
		OSImage:          fields["os"],
		KernelVersion:    fields["kernel"],
		Architecture:     normalizeArch(fields["arch"]),
		ContainerRuntime: fields["runtime"],
	}
	if n, err := strconv.ParseInt(fields["cpu"], 10, 32); err == nil {
		info.CPUCores = int32(n)
	}
	if kb, err := strconv.ParseInt(fields["memkb"], 10, 64); err == nil {
		info.MemoryMB = kb / 1024
	}
	return info, nil
}

// normalizeArch maps uname output onto Go/Kubernetes architecture names, so host
// facts can be compared against node labels and image tags directly.
func normalizeArch(uname string) string {
	switch uname {
	case "x86_64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return uname
	}
}
