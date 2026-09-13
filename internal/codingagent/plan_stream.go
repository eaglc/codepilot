package codingagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/eaglc/codepilot/internal/agent"
)

const maxPlanStreamArgumentBytes = 512 << 10

// planToolCallPreviewer projects only completed, display-safe Plan fields from
// partial exit_plan_mode arguments. Raw JSON fragments remain inside the Agent
// boundary and are never forwarded to presentation adapters.
type planToolCallPreviewer struct {
	names    map[string]string
	buffers  map[string][]byte
	rendered map[string]string
}

var _ agent.ToolCallStreamPreviewer = (*planToolCallPreviewer)(nil)

func newPlanToolCallPreviewer() *planToolCallPreviewer {
	return &planToolCallPreviewer{
		names: make(map[string]string), buffers: make(map[string][]byte), rendered: make(map[string]string),
	}
}

func (p *planToolCallPreviewer) PreviewToolCall(callID, toolName, delta string) (string, bool) {
	if p == nil || strings.TrimSpace(callID) == "" {
		return "", false
	}
	if toolName != "" {
		p.names[callID] = toolName
	}
	if p.names[callID] != exitPlanModeToolName || delta == "" {
		return "", false
	}
	if len(p.buffers[callID])+len(delta) > maxPlanStreamArgumentBytes {
		return "", false
	}
	p.buffers[callID] = append(p.buffers[callID], delta...)
	submission, found := partialPlanSubmission(p.buffers[callID])
	if !found {
		return "", false
	}
	preview := renderPlanStreamPreview(submission)
	if preview == "" || preview == p.rendered[callID] {
		return "", false
	}
	p.rendered[callID] = preview
	return preview, true
}

func partialPlanSubmission(raw []byte) (PlanSubmission, bool) {
	root := decodePartialJSON(raw)
	if root == nil || root.object == nil {
		return PlanSubmission{}, false
	}
	encoded, err := json.Marshal(root.value())
	if err != nil {
		return PlanSubmission{}, false
	}
	var submission PlanSubmission
	if json.Unmarshal(encoded, &submission) != nil {
		return PlanSubmission{}, false
	}
	return submission, true
}

type partialJSONNode struct {
	object map[string]*partialJSONNode
	array  []*partialJSONNode
	scalar any
}

type partialJSONFrame struct {
	node *partialJSONNode
	key  string
}

func decodePartialJSON(raw []byte) *partialJSONNode {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var root *partialJSONNode
	var stack []*partialJSONFrame
	attach := func(node *partialJSONNode) bool {
		if len(stack) == 0 {
			if root != nil {
				return false
			}
			root = node
			return true
		}
		parent := stack[len(stack)-1]
		if parent.node.object != nil {
			if parent.key == "" {
				return false
			}
			parent.node.object[parent.key] = node
			parent.key = ""
			return true
		}
		parent.node.array = append(parent.node.array, node)
		return true
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				node := &partialJSONNode{object: make(map[string]*partialJSONNode)}
				if !attach(node) {
					return root
				}
				stack = append(stack, &partialJSONFrame{node: node})
			case '[':
				node := &partialJSONNode{array: make([]*partialJSONNode, 0)}
				if !attach(node) {
					return root
				}
				stack = append(stack, &partialJSONFrame{node: node})
			case '}', ']':
				if len(stack) == 0 {
					return root
				}
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if len(stack) != 0 {
			parent := stack[len(stack)-1]
			if parent.node.object != nil && parent.key == "" {
				key, ok := token.(string)
				if !ok {
					return root
				}
				parent.key = key
				continue
			}
		}
		if !attach(&partialJSONNode{scalar: token}) {
			return root
		}
	}
	return root
}

func (n *partialJSONNode) value() any {
	if n == nil {
		return nil
	}
	if n.object != nil {
		value := make(map[string]any, len(n.object))
		for key, child := range n.object {
			value[key] = child.value()
		}
		return value
	}
	if n.array != nil {
		value := make([]any, len(n.array))
		for index, child := range n.array {
			value[index] = child.value()
		}
		return value
	}
	return n.scalar
}

func renderPlanStreamPreview(plan PlanSubmission) string {
	var body strings.Builder
	writeList := func(title string, values []string) {
		var normalized []string
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				normalized = append(normalized, value)
			}
		}
		if len(normalized) == 0 {
			return
		}
		fmt.Fprintf(&body, "\n\n### %s\n", title)
		for _, value := range normalized {
			fmt.Fprintf(&body, "\n- %s", value)
		}
	}
	if goal := strings.TrimSpace(plan.Goal); goal != "" {
		fmt.Fprintf(&body, "\n\n**Goal:** %s", goal)
	}
	writeList("Scope", plan.Scope.Included)
	writeList("Findings", plan.Findings)
	writeList("Assumptions", plan.Assumptions)
	writeList("Risks", plan.Risks)
	stepNumber := 0
	for _, step := range plan.Steps {
		goal := strings.TrimSpace(step.Goal)
		if goal == "" {
			continue
		}
		if stepNumber == 0 {
			body.WriteString("\n\n### Steps")
		}
		stepNumber++
		fmt.Fprintf(&body, "\n\n%d. %s", stepNumber, goal)
		for _, validation := range step.Validation {
			if validation = strings.TrimSpace(validation); validation != "" {
				fmt.Fprintf(&body, "\n   - Validate: %s", validation)
			}
		}
	}
	writeList("Acceptance criteria", plan.AcceptanceCriteria)
	if body.Len() == 0 {
		return ""
	}
	return "## Plan (drafting…)" + body.String()
}
