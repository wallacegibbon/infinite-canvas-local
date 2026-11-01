package sysinfo

import "testing"

// ComfyUI reports total and free; the panel wants used, in megabytes.
func TestFromComfyTurnsComfysAnswerIntoThePanelsUnits(t *testing.T) {
	stats := FromComfy(map[string]any{
		"system": map[string]any{"ram_total": float64(8 << 30), "ram_free": float64(2 << 30)},
		"devices": []any{
			map[string]any{"name": "cuda:0 RTX 5080", "index": float64(0), "vram_total": float64(16 << 30), "vram_free": float64(4 << 30)},
		},
	})
	if stats == nil {
		t.Fatal("FromComfy returned nil for a payload that carries a device")
	}
	if len(stats.GPU) != 1 {
		t.Fatalf("gpu = %v, want one device", stats.GPU)
	}
	if stats.GPU[0].MemoryTotalMb != 16*1024 {
		t.Fatalf("memoryTotalMb = %d, want %d", stats.GPU[0].MemoryTotalMb, 16*1024)
	}
	if stats.GPU[0].MemoryUsedMb != 12*1024 {
		t.Fatalf("memoryUsedMb = %d, want %d", stats.GPU[0].MemoryUsedMb, 12*1024)
	}
	if stats.Ram.TotalBytes != 8<<30 || stats.Ram.UsedBytes != 6<<30 {
		t.Fatalf("ram = %+v, want 8GiB total and 6GiB used", stats.Ram)
	}
	if stats.GPU[0].Name != "cuda:0 RTX 5080" {
		t.Fatalf("name = %q", stats.GPU[0].Name)
	}
}

func TestFromComfySaysNothingWhenThereIsNoDevice(t *testing.T) {
	if stats := FromComfy(map[string]any{"system": map[string]any{"ram_total": float64(1 << 30)}}); stats != nil {
		t.Fatalf("stats = %+v, want nil: a machine with no reported GPU has nothing to show", stats)
	}
	// A device with no memory figures is not a device the panel can draw.
	if stats := FromComfy(map[string]any{"devices": []any{map[string]any{"name": "cpu"}}}); stats != nil {
		t.Fatalf("stats = %+v, want nil", stats)
	}
}

func TestFromComfyNumbersDevicesThatDoNotNumberThemselves(t *testing.T) {
	stats := FromComfy(map[string]any{
		"devices": []any{
			map[string]any{"name": "first", "vram_total": float64(1 << 30), "vram_free": float64(1 << 30)},
			map[string]any{"name": "second", "vram_total": float64(1 << 30), "vram_free": float64(1 << 30)},
		},
	})
	if stats == nil || len(stats.GPU) != 2 {
		t.Fatalf("stats = %+v, want two devices", stats)
	}
	if stats.GPU[0].Index != 0 || stats.GPU[1].Index != 1 {
		t.Fatalf("indexes = %d, %d; want 0, 1", stats.GPU[0].Index, stats.GPU[1].Index)
	}
}
