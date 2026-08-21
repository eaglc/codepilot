package provider

import (
	"context"

	"github.com/eaglc/codepilot/internal/session"
)

// Secret is a short-lived credential value. Callers must never log it.
type Secret []byte

// CredentialLocation identifies where a credential was stored.
type CredentialLocation string

const (
	// CredentialInKeyring indicates durable storage in the OS keyring.
	CredentialInKeyring CredentialLocation = "keyring"
	// CredentialInMemory indicates process-local fallback storage.
	CredentialInMemory CredentialLocation = "memory"
)

// CredentialStore persists provider credentials without exposing storage details.
type CredentialStore interface {
	Get(ctx context.Context, profileID session.ProviderProfileID) (Secret, bool, error)
	Put(ctx context.Context, profileID session.ProviderProfileID, secret Secret) (CredentialLocation, error)
	Delete(ctx context.Context, profileID session.ProviderProfileID) error
}

// ProviderProfileStore persists secret-free provider profiles.
type ProviderProfileStore interface {
	ListProfiles(ctx context.Context) ([]Profile, error)
	LoadProfile(ctx context.Context, id session.ProviderProfileID) (Profile, error)
	SaveProfile(ctx context.Context, value Profile) error
	DeleteProfile(ctx context.Context, id session.ProviderProfileID) error
}
