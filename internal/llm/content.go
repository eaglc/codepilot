package llm

import "fmt"

// ContentType identifies one provider-neutral message content block.
type ContentType string

const (
	// ContentText contains visible natural-language text.
	ContentText ContentType = "text"
	// ContentImage contains encoded image bytes and a MIME type.
	ContentImage ContentType = "image"
	// ContentThinking contains provider-returned reasoning content.
	ContentThinking ContentType = "thinking"
	// ContentToolCall contains one complete assistant tool request.
	ContentToolCall ContentType = "tool_call"
)

// Content is a tagged union. Only fields belonging to Type may be populated.
type Content struct {
	Type     ContentType `json:"type"`
	Text     string      `json:"text,omitempty"`
	Data     []byte      `json:"data,omitempty"`
	MIMEType string      `json:"mime_type,omitempty"`
	Redacted bool        `json:"redacted,omitempty"`
	ToolCall *ToolCall   `json:"tool_call,omitempty"`
}

// Validate checks the content discriminant and required payload.
func (c Content) Validate() error {
	switch c.Type {
	case ContentText:
		if c.Text == "" {
			return fmt.Errorf("validate text content: text is empty")
		}
		if len(c.Data) != 0 || c.MIMEType != "" || c.ToolCall != nil || c.Redacted {
			return fmt.Errorf("validate text content: unrelated fields are populated")
		}
	case ContentImage:
		if len(c.Data) == 0 || c.MIMEType == "" {
			return fmt.Errorf("validate image content: data and MIME type are required")
		}
		if c.Text != "" || c.ToolCall != nil || c.Redacted {
			return fmt.Errorf("validate image content: unrelated fields are populated")
		}
	case ContentThinking:
		if c.Text == "" && !c.Redacted {
			return fmt.Errorf("validate thinking content: text is empty")
		}
		if len(c.Data) != 0 || c.MIMEType != "" || c.ToolCall != nil {
			return fmt.Errorf("validate thinking content: unrelated fields are populated")
		}
	case ContentToolCall:
		if c.ToolCall == nil {
			return fmt.Errorf("validate tool-call content: call is missing")
		}
		if err := c.ToolCall.Validate(); err != nil {
			return err
		}
		if c.Text != "" || len(c.Data) != 0 || c.MIMEType != "" || c.Redacted {
			return fmt.Errorf("validate tool-call content: unrelated fields are populated")
		}
	default:
		return fmt.Errorf("validate content: unsupported type %q", c.Type)
	}
	return nil
}

// Clone returns a defensive copy of the content block.
func (c Content) Clone() Content {
	clone := c
	clone.Data = append([]byte(nil), c.Data...)
	if c.ToolCall != nil {
		call := *c.ToolCall
		call.Arguments = cloneRawMessage(c.ToolCall.Arguments)
		clone.ToolCall = &call
	}
	return clone
}
