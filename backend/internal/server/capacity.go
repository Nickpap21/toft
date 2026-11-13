package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// ServiceAllocReader is a narrow interface the server service uses to query
// resource allocations from the service domain without creating an import cycle.
type ServiceAllocReader interface {
	SumAllocatedForServer(ctx context.Context, serverID string) (cpuCores float64, memoryMB float64, err error)
}

// CapacitySummary carries total, allocated, and live resource data for a server.
type CapacitySummary struct {
	ServerID           string  `json:"server_id"`
	EffectiveCPUCores  float64 `json:"effective_cpu_cores"`
	EffectiveMemoryMB  int64   `json:"effective_memory_mb"`
	AllocatedCPUCores  float64 `json:"allocated_cpu_cores"`
	AllocatedMemoryMB  float64 `json:"allocated_memory_mb"`
	LiveCPUPercent     float64 `json:"live_cpu_percent"`
	LiveMemoryMB       int64   `json:"live_memory_mb"`
	CapacityDiscovered bool    `json:"capacity_discovered"`
	// Share: the figures are the caller's share of a server another
	// workspace owns (D-39), not the server's.
	Share bool `json:"share"`
	// OwnerOrgID is the workspace that owns the server.
	OwnerOrgID string `json:"-"`
	// ServerName names the server in a capacity refusal.
	ServerName string `json:"-"`
	// The owner's view only, from the same SSH read as the live figures
	// (0 and "" when it failed), and what each workspace's services reserve.
	DiskUsedMB    int64                 `json:"disk_used_mb,omitempty"`
	DiskTotalMB   int64                 `json:"disk_total_mb,omitempty"`
	DockerVersion string                `json:"docker_version,omitempty"`
	UptimeSeconds int64                 `json:"uptime_seconds,omitempty"`
	TraefikStatus string                `json:"traefik_status,omitempty"`
	TraefikImage  string                `json:"traefik_image,omitempty"`
	Workspaces    []WorkspaceAllocation `json:"workspaces,omitempty"`
}

// WorkspaceAllocation is what one workspace's non-stopped services on a
// server reserve, and which of its projects they belong to.
type WorkspaceAllocation struct {
	OrgID    string   `json:"org_id"`
	OrgName  string   `json:"org_name"`
	Projects []string `json:"projects"`
	Services int      `json:"services"`
	CPUCores float64  `json:"cpu_cores"`
	MemoryMB float64  `json:"memory_mb"`
}

// Fits reports whether a service reserving cpuCores and memoryMB fits in
// what the summary leaves free. A resource the service does not reserve (0)
// always fits. The deploy capacity gate and the Servers page both ask this.
func (s *CapacitySummary) Fits(cpuCores, memoryMB float64) (cpuOK, memOK bool) {
	cpuOK = cpuCores == 0 || s.AllocatedCPUCores+cpuCores <= s.EffectiveCPUCores
	memOK = memoryMB == 0 || s.AllocatedMemoryMB+memoryMB <= float64(s.EffectiveMemoryMB)
	return cpuOK, memOK
}

// CapacityExceededError is a deploy, or a service created to be deployed at
// once, that needs more than the workspace has left on its server: the 409
// capacity_exceeded's data, and its message.
type CapacityExceededError struct {
	Code              string  `json:"code"`
	CanBypass         bool    `json:"can_bypass"`
	EffectiveCPUCores float64 `json:"effective_cpu_cores"`
	EffectiveMemoryMB float64 `json:"effective_memory_mb"`
	AllocatedCPUCores float64 `json:"allocated_cpu_cores"`
	AllocatedMemoryMB float64 `json:"allocated_memory_mb"`
	RequestedCPUCores float64 `json:"requested_cpu_cores"`
	RequestedMemoryMB float64 `json:"requested_memory_mb"`
	Server            string  `json:"-"`
}

// Error says what is short, as the panel's capacity dialog does: "Not
// enough room on qa-node: needs 512 MiB of memory, 0 MiB left of 3.7 GiB."
func (e *CapacityExceededError) Error() string {
	var short []string
	if e.RequestedMemoryMB > 0 && e.AllocatedMemoryMB+e.RequestedMemoryMB > e.EffectiveMemoryMB {
		short = append(short, fmt.Sprintf("needs %s of memory, %s left of %s", memory(e.RequestedMemoryMB),
			memory(max(e.EffectiveMemoryMB-e.AllocatedMemoryMB, 0)), memory(e.EffectiveMemoryMB)))
	}
	if e.RequestedCPUCores > 0 && e.AllocatedCPUCores+e.RequestedCPUCores > e.EffectiveCPUCores {
		short = append(short, fmt.Sprintf("needs %s, %s left of %s", cores(e.RequestedCPUCores),
			cores(max(e.EffectiveCPUCores-e.AllocatedCPUCores, 0)), cores(e.EffectiveCPUCores)))
	}
	name := e.Server
	if name == "" {
		name = "its server"
	}
	return fmt.Sprintf("Not enough room on %s: %s.", name, strings.Join(short, "; "))
}

// memory is MiB under 1 GiB, else GiB with one decimal.
func memory(mb float64) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GiB", mb/1024)
	}
	return fmt.Sprintf("%.0f MiB", mb)
}

func cores(n float64) string {
	s := strconv.FormatFloat(math.Round(n*100)/100, 'f', -1, 64)
	if n == 1 {
		return s + " core"
	}
	return s + " cores"
}

// workspaceAllocReader splits a server's reservations by workspace.
// Satisfied by service.PostgresRepository.
type workspaceAllocReader interface {
	WorkspaceAllocations(ctx context.Context, serverID string) ([]WorkspaceAllocation, error)
}

// DiscoverCapacity reads nproc and free -m over the server's pooled SSH
// connection and persists the values. Manual overrides are left untouched.
func (s *Service) DiscoverCapacity(ctx context.Context, serverID string) (*Server, error) {
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}

	const script = `printf 'cpu:%s\nmem:%s\n' "$(nproc)" "$(free -m | awk 'NR==2{print $2}')"`
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	out, err := s.SSH(srv).Exec(ctx, "echo "+encoded+" | base64 -d | sh")
	if err != nil {
		return nil, fmt.Errorf("discover capacity: %w", err)
	}

	cores, memMB, err := parseDiscoveryOutput(out)
	if err != nil {
		return nil, err
	}
	// Scan again reads what the server can run too (D-93).
	if s.caps != nil {
		if _, perr := s.ProbeCapabilities(ctx, serverID); perr != nil {
			s.log.Warn("probe capabilities", zap.Error(perr), zap.String("server_id", serverID))
		}
	}

	return s.repo.UpdateDiscoveredCapacity(ctx, serverID, cores, memMB)
}

// UpdateCapacity persists manual capacity overrides. Pass nil for a field to clear its override.
func (s *Service) UpdateCapacity(ctx context.Context, serverID string, input UpdateCapacityInput) (*Server, error) {
	return s.repo.UpdateCapacity(ctx, serverID, input)
}

// GetCapacitySummary returns total, allocated, and live resource data for a server.
func (s *Service) GetCapacitySummary(ctx context.Context, serverID string) (*CapacitySummary, error) {
	srv, err := s.repo.GetByID(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}

	summary := &CapacitySummary{
		ServerID:           serverID,
		OwnerOrgID:         srv.OrgID,
		EffectiveCPUCores:  srv.EffectiveCPUCores(),
		EffectiveMemoryMB:  srv.EffectiveMemoryMB(),
		CapacityDiscovered: srv.TotalCPUCores != nil && *srv.TotalCPUCores > 0,
	}

	if s.allocReader != nil {
		allocCPU, allocMem, err := s.allocReader.SumAllocatedForServer(ctx, serverID)
		if err != nil {
			log.Printf("server %s: failed to read allocated resources: %v", serverID, err)
		} else {
			summary.AllocatedCPUCores = allocCPU
			summary.AllocatedMemoryMB = allocMem
		}
	}
	if r, ok := s.allocReader.(workspaceAllocReader); ok {
		ws, err := r.WorkspaceAllocations(ctx, serverID)
		if err != nil {
			return nil, fmt.Errorf("workspace allocations: %w", err)
		}
		summary.Workspaces = ws
	}

	// Best-effort — don't fail the whole request if SSH is temporarily down.
	if stats, err := s.GetSystemStats(ctx, serverID); err == nil {
		summary.LiveCPUPercent = stats.CPUPercent
		summary.LiveMemoryMB = stats.MemUsedMB
		summary.DiskUsedMB, summary.DiskTotalMB = stats.DiskUsedMB, stats.DiskTotalMB
		summary.DockerVersion = stats.DockerVersion
		summary.UptimeSeconds = stats.UptimeSeconds
		summary.TraefikStatus, summary.TraefikImage = stats.TraefikStatus, stats.TraefikImage
	}

	return summary, nil
}

// ListServicesOnServer returns orgID's services on this server.
func (s *Service) ListServicesOnServer(ctx context.Context, serverID, orgID string) ([]ServiceOnServer, error) {
	return s.repo.ListServicesByServer(ctx, serverID, orgID)
}

func parseDiscoveryOutput(raw string) (cores float64, memMB int64, err error) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		key, val := line[:idx], strings.TrimSpace(line[idx+1:])
		switch key {
		case "cpu":
			cores, err = strconv.ParseFloat(val, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("parse cpu cores %q: %w", val, err)
			}
		case "mem":
			memMB, err = strconv.ParseInt(val, 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("parse memory mb %q: %w", val, err)
			}
		}
	}
	if cores == 0 || memMB == 0 {
		return 0, 0, fmt.Errorf("no capacity data in output: %q", raw)
	}
	return cores, memMB, nil
}

// FitResult is whether a service with the asked limits fits on a server for
// a workspace, decided as the deploy capacity gate decides, and the room it
// was measured against: the workspace's share, or for the owner the server
// less its shares.
type FitResult struct {
	CapacityKnown     bool    `json:"capacity_known"`
	Fits              bool    `json:"fits"`
	CPUOK             bool    `json:"cpu_ok"`
	MemoryOK          bool    `json:"memory_ok"`
	Share             bool    `json:"share"`
	EffectiveCPUCores float64 `json:"effective_cpu_cores"`
	EffectiveMemoryMB int64   `json:"effective_memory_mb"`
	AllocatedCPUCores float64 `json:"allocated_cpu_cores"`
	AllocatedMemoryMB float64 `json:"allocated_memory_mb"`
}

// Fit answers FitResult for orgID, which must own the server or have a
// share of it. Capacity never scanned fits everything, as in the gate; a
// server still waiting for its agent has no room to answer for
// (ErrServerNotReady).
func (s *Service) Fit(ctx context.Context, serverID, orgID string, cpuCores, memoryMB float64) (*FitResult, error) {
	if err := s.CheckReady(ctx, serverID); err != nil {
		return nil, err
	}
	sum, err := s.GetCapacitySummaryFor(ctx, serverID, orgID)
	if err != nil {
		return nil, err
	}
	r := &FitResult{
		CapacityKnown: sum.CapacityDiscovered, Share: sum.Share, CPUOK: true, MemoryOK: true,
		EffectiveCPUCores: sum.EffectiveCPUCores, EffectiveMemoryMB: sum.EffectiveMemoryMB,
		AllocatedCPUCores: sum.AllocatedCPUCores, AllocatedMemoryMB: sum.AllocatedMemoryMB,
	}
	if sum.CapacityDiscovered {
		r.CPUOK, r.MemoryOK = sum.Fits(cpuCores, memoryMB)
	}
	r.Fits = r.CPUOK && r.MemoryOK
	return r, nil
}
