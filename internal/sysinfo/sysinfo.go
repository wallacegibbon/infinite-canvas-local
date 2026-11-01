// Package sysinfo is the shape of the system panel's numbers: what the machine that runs ComfyUI
// has left to give.
//
// It deliberately does not sample the gateway's own host. The gateway and ComfyUI are two machines
// as often as one, and a figure about the wrong machine is worse than no figure at all: it reads
// "1.7 GB of VRAM free" next to a generation that just ran out of memory. ComfyUI already answers
// the question about itself at /system_stats — this package turns that answer into the panel's
// units, and nothing else.
package sysinfo

import (
	"encoding/json"

	"comfyui-gateway/internal/util"
)

// GPU is one device as ComfyUI sees it. There is no load or temperature here because ComfyUI does
// not report them; the panel shows what it is told and leaves the rest out.
type GPU struct {
	Index         int    `json:"index"`
	Name          string `json:"name"`
	MemoryUsedMb  int    `json:"memoryUsedMb"`
	MemoryTotalMb int    `json:"memoryTotalMb"`
}

// Ram is memory on ComfyUI's host, in bytes.
type Ram struct {
	TotalBytes int64 `json:"totalBytes"`
	FreeBytes  int64 `json:"freeBytes"`
	UsedBytes  int64 `json:"usedBytes"`
}

// Stats is one reading of ComfyUI's host.
type Stats struct {
	At  string `json:"at"`
	GPU []GPU  `json:"gpu"`
	Ram Ram    `json:"ram"`
}

// FromComfy turns a /system_stats payload into a reading.
//
// It returns nil when ComfyUI did not say what its devices have: a build that reports no GPU is one
// the panel has nothing honest to show for, and "0 GB used of 0 GB" would be a worse answer than
// silence.
func FromComfy(payload map[string]any) *Stats {
	devices, _ := payload["devices"].([]any)
	stats := &Stats{At: util.NowIso(), GPU: []GPU{}}

	for position, raw := range devices {
		device, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		total := int64(number(device["vram_total"]))
		if total <= 0 {
			continue
		}
		free := int64(number(device["vram_free"]))
		used := total - free
		if used < 0 {
			used = 0
		}
		// ComfyUI names the device and numbers it; a build that leaves the index out gets the
		// position it was listed at, so the panel never shows two "GPU 0"s.
		index := int(number(device["index"]))
		if index == 0 && position > 0 {
			index = position
		}
		stats.GPU = append(stats.GPU, GPU{
			Index:         index,
			Name:          text(device["name"]),
			MemoryUsedMb:  int(used / (1024 * 1024)),
			MemoryTotalMb: int(total / (1024 * 1024)),
		})
	}
	if len(stats.GPU) == 0 {
		return nil
	}

	system, _ := payload["system"].(map[string]any)
	total := int64(number(system["ram_total"]))
	if total > 0 {
		free := int64(number(system["ram_free"]))
		used := total - free
		if used < 0 {
			used = 0
		}
		stats.Ram = Ram{TotalBytes: total, FreeBytes: free, UsedBytes: used}
	}
	return stats
}

// number reads a JSON number, whatever the decoder produced for it.
func number(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case json.Number:
		parsed, err := typed.Float64()
		if err == nil {
			return parsed
		}
	}
	return 0
}

func text(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}
