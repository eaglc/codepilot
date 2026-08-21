package tool

import (
	"context"
	"encoding/json"
)

// Tool is the provider-neutral execution contract used by the agent runtime.
type Tool interface {
	Definition() Definition
	Invoke(ctx context.Context, arguments json.RawMessage) (Result, error)
}
