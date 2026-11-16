package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type cpuCounter struct{ total, idle uint64 }
type runtimeStats struct {
	CPUPct      float64 `json:"cpu_percent"`
	Memory      int64   `json:"memory_bytes"`
	MemoryLimit int64   `json:"memory_limit_bytes"`
	PIDs        uint64  `json:"pids"`
	Available   bool    `json:"resource_available"`
}

var hostCPUState struct {
	sync.Mutex
	last        cpuCounter
	initialized bool
}

func (a *API) hostMetrics(ctx context.Context) map[string]any {
	cpu := hostCPUPercent()
	memory, memoryPct := hostMemory()
	diskPct, diskUsed, diskTotal := hostDisk(env("DATA_ROOT", "/var/lib/aihub/data"))
	info := struct {
		NCPU          int    `json:"NCPU"`
		MemTotal      uint64 `json:"MemTotal"`
		ServerVersion string `json:"ServerVersion"`
		Containers    int    `json:"Containers"`
		Images        int    `json:"Images"`
	}{}
	status := "节点指标可用"
	if err := a.dockerGet(ctx, "/info", &info); err != nil {
		status = "Docker API 不可用"
	}
	return map[string]any{"cpu_percent": cpu, "memory_percent": memoryPct, "memory_bytes": memory, "disk_percent": diskPct, "disk_used_bytes": diskUsed, "disk_total_bytes": diskTotal, "docker_version": info.ServerVersion, "docker_containers": info.Containers, "docker_images": info.Images, "status": status, "sampled_at": time.Now().UTC()}
}

func (a *API) dockerGet(ctx context.Context, path string, out any) error {
	base := strings.TrimRight(env("DOCKER_API_RO", "http://docker-proxy-ro:2375"), "/")
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1.44"+path, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return errDockerAPI
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

var errDockerAPI = &metricsError{"Docker API request failed"}

type metricsError struct{ message string }

func (e *metricsError) Error() string { return e.message }

func hostCPUPercent() float64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	fields := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
	if len(fields) < 5 {
		return 0
	}
	var values []uint64
	for _, field := range fields[1:] {
		n, _ := strconv.ParseUint(field, 10, 64)
		values = append(values, n)
	}
	var current cpuCounter
	for _, n := range values {
		current.total += n
	}
	current.idle = values[3]
	if len(values) > 4 {
		current.idle += values[4]
	}
	hostCPUState.Lock()
	defer hostCPUState.Unlock()
	previous := hostCPUState.last
	hostCPUState.last = current
	if !hostCPUState.initialized {
		hostCPUState.initialized = true
		return 0
	}
	totalDelta := current.total - previous.total
	if totalDelta == 0 {
		return 0
	}
	idleDelta := current.idle - previous.idle
	return round2(float64(totalDelta-idleDelta) * 100 / float64(totalDelta))
}

func hostMemory() (int64, float64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	values := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if n, e := strconv.ParseUint(fields[1], 10, 64); e == nil {
				values[strings.TrimSuffix(fields[0], ":")] = n * 1024
			}
		}
	}
	total, available := values["MemTotal"], values["MemAvailable"]
	if total == 0 {
		return 0, 0
	}
	used := total - available
	return int64(used), round2(float64(used) * 100 / float64(total))
}

func hostDisk(path string) (float64, int64, int64) {
	var stat syscall.Statfs_t
	if syscall.Statfs(path, &stat) != nil {
		return 0, 0, 0
	}
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	if total == 0 {
		return 0, 0, 0
	}
	used := total - free
	return round2(float64(used) * 100 / float64(total)), int64(used), int64(total)
}
func round2(value float64) float64 { return float64(int(value*100+0.5)) / 100 }

func (a *API) containerStats(ctx context.Context, id string) runtimeStats {
	var result runtimeStats
	if id == "" {
		return result
	}
	var data struct {
		CPUStats struct {
			CPUUsage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			System uint64 `json:"system_cpu_usage"`
			Online uint32 `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPUStats struct {
			CPUUsage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			System uint64 `json:"system_cpu_usage"`
		} `json:"precpu_stats"`
		MemoryStats struct {
			Usage uint64            `json:"usage"`
			Limit uint64            `json:"limit"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
		PIDsStats struct {
			Current uint64 `json:"current"`
		} `json:"pids_stats"`
	}
	if err := a.dockerGet(ctx, "/containers/"+id+"/stats?stream=false", &data); err != nil {
		return result
	}
	mem := data.MemoryStats.Usage
	if inactive := data.MemoryStats.Stats["inactive_file"]; inactive < mem {
		mem -= inactive
	}
	cpuDelta := data.CPUStats.CPUUsage.Total - data.PreCPUStats.CPUUsage.Total
	systemDelta := data.CPUStats.System - data.PreCPUStats.System
	cpus := data.CPUStats.Online
	if cpus == 0 {
		cpus = 1
	}
	if systemDelta > 0 {
		result.CPUPct = round2(float64(cpuDelta) / float64(systemDelta) * float64(cpus) * 100)
	}
	result.Memory = int64(mem)
	result.MemoryLimit = int64(data.MemoryStats.Limit)
	result.PIDs = data.PIDsStats.Current
	result.Available = true
	return result
}
