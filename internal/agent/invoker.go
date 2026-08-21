package agent

import "context"

// AgentInvoker runs provider-neutral model/tool loops and resumptions.
type AgentInvoker interface {
	Invoke(ctx context.Context, input InvocationInput, events InvocationEventSink) (InvocationResult, error)
	Resume(ctx context.Context, input ResumeInput, events InvocationEventSink) (InvocationResult, error)
	Close() error
}

// InvocationEventSink receives ordered, provider-neutral invocation events.
type InvocationEventSink interface {
	PublishInvocationEvent(ctx context.Context, event InvocationEvent) error
}

// AgentInvokerFactory creates one invoker owned by one CodingAgent.
type AgentInvokerFactory interface {
	CreateInvoker(ctx context.Context) (AgentInvoker, error)
}
