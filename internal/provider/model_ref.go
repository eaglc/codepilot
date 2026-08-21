package provider

// ModelRef identifies one configured provider profile and model without
// exposing credentials or provider-specific configuration.
type ModelRef struct {
	Provider string
	Model    string
}
