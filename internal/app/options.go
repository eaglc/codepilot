package app

import "io"

// Options contains process-owned inputs used to build CodePilot.
type Options struct {
	WorkingDirectory string
	ConfigDir        string
	StateDir         string
	TrustWorkspace   bool

	// Input and Output are optional embedding and test overrides. Nil uses the
	// process terminal selected by Bubble Tea.
	Input  io.Reader
	Output io.Writer

	// DisableInput is intended for embedding and smoke tests that terminate via
	// context cancellation instead of a terminal keyboard.
	DisableInput bool
}
