package agent

import (
	"context"
	"testing"
	"time"

	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/tool"
)

func TestValidateInvocationInputRejectsBusinessBoundaryForgery(t *testing.T) {
	t.Parallel()
	input := validInvocationInput(tool.NewRegistry())
	sink := &recordingInvocationSink{}

	tests := []struct {
		name   string
		mutate func(*InvocationInput)
	}{
		{name: "missing invocation ID", mutate: func(value *InvocationInput) { value.ID = "" }},
		{name: "missing model", mutate: func(value *InvocationInput) { value.Model.Model = "" }},
		{name: "oversized model", mutate: func(value *InvocationInput) { value.Model.Model = string(make([]byte, maxInvocationIDBytes+1)) }},
		{name: "invalid role", mutate: func(value *InvocationInput) { value.Messages[0].Role = "tool" }},
		{name: "empty message", mutate: func(value *InvocationInput) { value.Messages[0].Content = " " }},
		{name: "excessive steps", mutate: func(value *InvocationInput) { value.Limits.MaxSteps = maxInvocationSteps + 1 }},
		{name: "excessive duration", mutate: func(value *InvocationInput) { value.Limits.MaxDuration = maxInvocationDuration + time.Second }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := input
			value.Messages = append([]InvocationMessage(nil), input.Messages...)
			test.mutate(&value)
			if err := validateInvocationInput(value, sink); err == nil {
				t.Fatal("validateInvocationInput() error = nil")
			}
		})
	}
}

func TestValidateResumeInputRejectsUnknownResponse(t *testing.T) {
	t.Parallel()
	err := validateResumeInput(ResumeInput{
		CheckpointID: "checkpoint-1",
		InterruptID:  "interrupt-1",
		Response:     InterruptResponse{Kind: "later"},
	}, &recordingInvocationSink{})
	if err == nil {
		t.Fatal("validateResumeInput() error = nil")
	}
}

func validInvocationInput(registry *tool.Registry) InvocationInput {
	return InvocationInput{
		ID:           "turn-1",
		CheckpointID: "checkpoint-1",
		Model:        provider.ModelRef{Provider: "test", Model: "scripted"},
		SystemPrompt: "Use the available tools.",
		Messages:     []InvocationMessage{{Role: InvocationRoleUser, Content: "fix it"}},
		Tools:        registry,
		Limits:       InvocationLimits{MaxSteps: 5, MaxDuration: 5 * time.Second},
	}
}

type recordingInvocationSink struct {
	events []InvocationEvent
}

func (s *recordingInvocationSink) PublishInvocationEvent(_ context.Context, event InvocationEvent) error {
	s.events = append(s.events, cloneInvocationEvent(event))
	return nil
}

func cloneInvocationEvent(event InvocationEvent) InvocationEvent {
	if event.Tool != nil {
		value := *event.Tool
		event.Tool = &value
	}
	if event.Interrupt != nil {
		value := *event.Interrupt
		value.Payload = append([]byte(nil), event.Interrupt.Payload...)
		event.Interrupt = &value
	}
	return event
}
