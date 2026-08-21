package tool

import "encoding/json"

// Definition describes a tool without exposing trusted runtime parameters.
type Definition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}
