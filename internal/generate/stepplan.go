// Package generate turns a resolved workflow plus a request into ComfyUI output.
// It owns LoRA injection, step planning, preview relay and the polling runner.
package generate

import (
	"math"
	"strconv"
	"strings"

	"comfyui-gateway/internal/workflow"
)

// StepPlan describes how to map sampler progress onto one continuous bar.
type StepPlan struct {
	Total int
	Steps map[string]int
	// Absolute marks nodes whose reported value is a global step index rather than
	// a per-node fraction. SelfLift works that way: it reports the low-res stage
	// against its own shorter total, then continues from transition_step.
	Absolute map[string]bool
}

// EstimateStepPlan counts the sampling steps of every sampler in a graph.
func EstimateStepPlan(graph workflow.Graph) StepPlan {
	plan := StepPlan{Steps: map[string]int{}, Absolute: map[string]bool{}}
	for _, id := range graph.IDs() {
		node, ok := graph.NodeAt(id)
		if !ok {
			continue
		}
		inputs := node.Inputs()
		value := 0
		switch node.ClassType() {
		case "KSampler":
			value = positiveInt(inputs["steps"])
		case "KSamplerAdvanced":
			count := positiveInt(inputs["steps"])
			start := maxFloat(0, numberOr(inputs["start_at_step"], 0))
			end := minFloat(float64(count), numberOr(inputs["end_at_step"], float64(count)))
			value = int(maxFloat(0, end-start))
		case "SamplerCustomAdvanced":
			value = sigmaSteps(graph, linkedNode(graph, inputs["sigmas"]))
		case "SelfLiftH3Sampler":
			value = sigmaSteps(graph, linkedNode(graph, inputs["sigmas"]))
			if value > 0 {
				plan.Absolute[id] = true
			}
		}
		if value > 0 {
			plan.Steps[id] = value
			plan.Total += value
		}
	}
	return plan
}

// sigmaSteps derives a step count from whichever scheduler node feeds a sampler.
func sigmaSteps(graph workflow.Graph, node workflow.Node) int {
	if node == nil {
		return 0
	}
	inputs := node.Inputs()
	switch node.ClassType() {
	case "ManualSigmas":
		raw, _ := inputs["sigmas"].(string)
		count := 0
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n'
		}) {
			if _, err := strconv.ParseFloat(part, 64); err == nil {
				count++
			}
		}
		if count > 0 {
			return count - 1
		}
		return 0
	case "BasicScheduler":
		return positiveInt(inputs["steps"])
	case "H3SigmaRefiner":
		return sigmaSteps(graph, linkedNode(graph, inputs["sigmas"])) + positiveInt(inputs["extra_steps"])
	}
	return 0
}

func linkedNode(graph workflow.Graph, input any) workflow.Node {
	list, ok := input.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	nodeID, ok := list[0].(string)
	if !ok {
		return nil
	}
	node, _ := graph.NodeAt(nodeID)
	return node
}

func positiveInt(value any) int {
	number := numberOr(value, 0)
	if number <= 0 {
		return 0
	}
	return int(number)
}

func numberOr(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// jsRound matches JavaScript's Math.round: halves round toward positive infinity.
func jsRound(value float64) float64 {
	return math.Floor(value + 0.5)
}
