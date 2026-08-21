package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/eaglc/codepilot/internal/tool"
)

var (
	errToolInvoke   = errors.New("tool invoke failed")
	errEventPublish = errors.New("event publish failed")
)

type erroringTool struct{}

func (erroringTool) Definition() tool.Definition {
	return tool.Definition{Name: "boom", Description: "boom"}
}

func (erroringTool) Invoke(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{}, errToolInvoke
}

type failingInvocationSink struct{}

func (failingInvocationSink) PublishInvocationEvent(_ context.Context, event InvocationEvent) error {
	if event.Kind == InvocationEventToolFinished {
		return errEventPublish
	}
	return nil
}

func TestEinoRegistryToolJoinsToolFailureWithReportFailure(t *testing.T) {
	toolValue := &einoRegistryTool{
		name:   "boom",
		source: erroringTool{},
		relay:  newInvocationEventRelay(failingInvocationSink{}),
	}

	_, err := toolValue.InvokableRun(context.Background(), "{}")

	if !errors.Is(err, errToolInvoke) {
		t.Fatalf("error = %v, want to wrap the tool failure", err)
	}
	if !errors.Is(err, errEventPublish) {
		t.Fatalf("error = %v, want to wrap the report publish failure", err)
	}
}
