package session

import "time"

// MessageRole identifies the author of a neutral persisted message.
type MessageRole string

const (
	// RoleUser marks a message submitted by the user.
	RoleUser MessageRole = "user"
	// RoleAssistant marks a final assistant message.
	RoleAssistant MessageRole = "assistant"
)

// Message is a provider-neutral user or assistant message.
type Message struct {
	ID        MessageID
	SessionID SessionID
	TurnID    TurnID
	Role      MessageRole
	Content   string
	CreatedAt time.Time
}
