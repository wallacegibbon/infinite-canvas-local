// Package sysinfo reports host memory and GPU statistics for the system panel.
package sysinfo

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/util"
)

// GPU is one NVIDIA device.
type GPU struct {
	Index         int    `json:"index"`
	Name          string `json:"name"`
	MemoryUsedMb  int    `json:"memoryUsedMb"`
	MemoryTotalMb int    `json:"memoryTotalMb"`
	Utilization   int    `json:"utilization"`
	Temperature   int    `json:"temperature"`
}

// Ram is host memory usage in bytes.
type Ram struct {
	TotalBytes int64 `json:"totalBytes"`
	FreeBytes  int64 `json:"freeBytes"`
	UsedBytes  int64 `json:"usedBytes"`
}

// Stats is one sample of host resources.
type Stats struct {
	At  string `json:"at"`
	GPU []GPU  `json:"gpu"`
	// GPUError is a pointer so a healthy host serialises null, not an empty string;
	// the canvas distinguishes "no GPU" from "GPU query failed".
	GPUError *string `json:"gpuError"`
	Ram      Ram     `json:"ram"`
}

// gpuFields asks nvidia-smi for exactly what the UI renders.
const gpuFields = "index,name,memory.used,memory.total,utilization.gpu,temperature.gpu"

// Reader samples host resources with a short-lived cache, because the system panel
// polls while a user is also generating, and shelling out to nvidia-smi per request
// would itself become load.
type Reader struct {
	mu       sync.Mutex
	cached   *Stats
	cachedAt time.Time
}

// NewReader builds a Reader.
func NewReader() *Reader { return &Reader{} }

// Read returns a sample, reusing a value younger than cacheMs.
func (r *Reader) Read(cacheMs int) *Stats {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cached != nil && cacheMs > 0 && time.Since(r.cachedAt) < time.Duration(cacheMs)*time.Millisecond {
		return r.cached
	}

	sample := &Stats{At: util.NowIso(), GPU: []GPU{}}
	gpu, err := readGPU()
	if err != nil {
		message := err.Error()
		sample.GPUError = &message
	} else {
		sample.GPU = gpu
	}
	sample.Ram = readRam()

	r.cached = sample
	r.cachedAt = time.Now()
	return sample
}

// readGPU shells out to nvidia-smi. A missing binary is not an error condition:
// the host may simply have no NVIDIA GPU.
func readGPU() ([]GPU, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu="+gpuFields, "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, err
	}

	var devices []GPU
	for index, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) < 6 {
			continue
		}
		device := GPU{Name: parts[1]}
		device.Index = atoiOr(parts[0], index)
		device.MemoryUsedMb = atoiOr(parts[2], 0)
		device.MemoryTotalMb = atoiOr(parts[3], 0)
		device.Utilization = atoiOr(parts[4], 0)
		device.Temperature = atoiOr(parts[5], 0)
		devices = append(devices, device)
	}
	if devices == nil {
		devices = []GPU{}
	}
	return devices, nil
}

func atoiOr(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

// readRam parses /proc/meminfo.
//
// MemAvailable is used rather than MemFree: MemFree excludes reclaimable page cache,
// which makes "used" read as nearly the whole machine and makes the freed-bytes
// figure from /api/system/free-memory pure noise.
func readRam() Ram {
	data, err := readFile("/proc/meminfo")
	if err != nil {
		return Ram{}
	}
	var totalKB, availableKB int64
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalKB = value
		case "MemAvailable:":
			availableKB = value
		}
	}
	if totalKB == 0 {
		return Ram{}
	}
	total := totalKB * 1024
	available := availableKB * 1024
	used := total - available
	if used < 0 {
		used = 0
	}
	return Ram{TotalBytes: total, FreeBytes: available, UsedBytes: used}
}
