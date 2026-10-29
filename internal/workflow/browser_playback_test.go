package workflow

// Browser-playback guard for the workflows shipped in this repository.
//
// Browsers have no HEVC decoder and no 10-bit H.264 support, so a video saved as
// H.265 or as yuv420p10le plays its audio track and shows a black frame. That failure
// is silent: the file is perfectly valid and opens in VLC. This test fails the build
// instead, so a re-export from ComfyUI cannot quietly reintroduce it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shippedWorkflowsDir is the on-disk workflow folder next to this package.
const shippedWorkflowsDir = "../../workflows"

// videoSaveNode reports whether a node writes a video file, and if so its inputs.
func videoSaveNode(node map[string]any) (map[string]any, bool) {
	classType, _ := node["class_type"].(string)
	switch classType {
	case "VHS_VideoCombine", "SaveVideo", "SaveWebM", "SaveAnimatedWEBP":
		inputs, _ := node["inputs"].(map[string]any)
		return inputs, true
	}
	return nil, false
}

func TestShippedVideoWorkflowsAreBrowserPlayable(t *testing.T) {
	if _, err := os.Stat(shippedWorkflowsDir); err != nil {
		t.Skipf("shipped workflows not available: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(shippedWorkflowsDir, "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Skip("no workflow files found")
	}

	checked := 0
	for _, file := range files {
		if filepath.Base(file) == registryFileName {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		var graph map[string]any
		if err := json.Unmarshal(data, &graph); err != nil {
			// A UI-format export is not a graph the gateway can use; skip it here.
			continue
		}

		for nodeID, raw := range graph {
			node, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			inputs, isSave := videoSaveNode(node)
			if !isSave {
				continue
			}
			checked++

			format, _ := inputs["format"].(string)
			// "auto" is accepted: it preserves a compatible source stream, and every
			// workflow that produces a source now produces H.264.
			if strings.Contains(strings.ToLower(format), "265") ||
				strings.Contains(strings.ToLower(format), "hevc") {
				t.Errorf("%s node %s uses %q: browsers cannot decode HEVC, the video will play audio only",
					filepath.Base(file), nodeID, format)
			}

			pixFmt, _ := inputs["pix_fmt"].(string)
			if strings.HasSuffix(strings.ToLower(pixFmt), "10le") ||
				strings.HasSuffix(strings.ToLower(pixFmt), "12le") {
				t.Errorf("%s node %s uses pix_fmt %q: browsers do not support 10/12-bit H.264",
					filepath.Base(file), nodeID, pixFmt)
			}
		}
	}
	if checked == 0 {
		t.Error("no video save node was inspected; the guard is not looking at the right nodes")
	}
	t.Logf("inspected %d video save node(s) across %d workflow file(s)", checked, len(files))
}
