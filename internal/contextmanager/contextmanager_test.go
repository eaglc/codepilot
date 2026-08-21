package contextmanager

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestNopStrategyProcessReturnsIsolatedContext(t *testing.T) {
	request := Request{SystemPrompt: "system", Messages: []Message{{ID: "message-1", Role: RoleUser, Content: "question", Current: true}}}
	result, err := (NopStrategy{}).Process(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.SystemPrompt != request.SystemPrompt || !reflect.DeepEqual(result.Messages, request.Messages) {
		t.Fatalf("nop result = %#v", result)
	}
	result.Messages[0].Content = "changed"
	if request.Messages[0].Content != "question" {
		t.Fatal("nop strategy exposed the caller's message slice")
	}
}

func TestManagerProcessAppliesStrategiesInOrder(t *testing.T) {
	order := make([]string, 0, 2)
	manager, err := NewManager(
		testStrategy{name: "first", order: &order},
		testStrategy{name: "second", order: &order},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Process(context.Background(), Request{
		Scope:        Scope{SessionID: "session-1", TurnID: "turn-1"},
		SystemPrompt: "system",
		Messages:     []Message{{ID: "message-1", Role: RoleUser, Content: "question", Current: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"first", "second"}) {
		t.Fatalf("strategy order = %q", order)
	}
	if result.SystemPrompt != "system|first|second" || result.Messages[0].Content != "question|first|second" {
		t.Fatalf("composed result = %#v", result)
	}
}

type testStrategy struct {
	name  string
	order *[]string
}

func (s testStrategy) Process(_ context.Context, request Request) (Result, error) {
	*s.order = append(*s.order, s.name)
	messages := cloneMessages(request.Messages)
	for index := range messages {
		messages[index].Content = fmt.Sprintf("%s|%s", messages[index].Content, s.name)
	}
	return Result{SystemPrompt: fmt.Sprintf("%s|%s", request.SystemPrompt, s.name), Messages: messages}, nil
}
