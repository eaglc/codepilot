package credential

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/provider"
	keyring "github.com/zalando/go-keyring"
)

type fakeKeyring struct {
	values    map[string]string
	getError  error
	setError  error
	deleteErr error
}

func (f *fakeKeyring) Get(_, user string) (string, error) {
	if f.getError != nil {
		return "", f.getError
	}
	value, found := f.values[user]
	if !found {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (f *fakeKeyring) Set(_, user, password string) error {
	if f.setError != nil {
		return f.setError
	}
	f.values[user] = password
	return nil
}

func (f *fakeKeyring) Delete(_, user string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, found := f.values[user]; !found {
		return keyring.ErrNotFound
	}
	delete(f.values, user)
	return nil
}

func TestEnvironmentStoreUsesExplicitReferenceMapping(t *testing.T) {
	store, err := newEnvironmentStore(map[string]string{"openai": "OPENAI_API_KEY"}, func(name string) (string, bool) {
		if name != "OPENAI_API_KEY" {
			t.Fatalf("unexpected environment lookup %q", name)
		}
		return "environment-secret", true
	})
	if err != nil {
		t.Fatalf("create environment store: %v", err)
	}
	value, found, err := store.LoadCredential(context.Background(), "openai")
	if err != nil || !found || string(value) != "environment-secret" {
		t.Fatalf("load mapped credential: value=%q found=%v err=%v", value, found, err)
	}
	if _, found, err := store.LoadCredential(context.Background(), "unknown"); err != nil || found {
		t.Fatalf("unknown reference: found=%v err=%v", found, err)
	}
}

func TestKeyringStoreSaveOverwriteLoadAndDelete(t *testing.T) {
	backend := &fakeKeyring{values: make(map[string]string)}
	store, err := newKeyringStore("CodePilot Test", backend)
	if err != nil {
		t.Fatalf("create Keyring store: %v", err)
	}
	ctx := context.Background()
	if err := store.SaveCredential(ctx, "openai", provider.Credential("first")); err != nil {
		t.Fatalf("save credential: %v", err)
	}
	if err := store.SaveCredential(ctx, "openai", provider.Credential("second")); err != nil {
		t.Fatalf("overwrite credential: %v", err)
	}
	value, found, err := store.LoadCredential(ctx, "openai")
	if err != nil || !found || string(value) != "second" {
		t.Fatalf("load credential: value=%q found=%v err=%v", value, found, err)
	}
	if err := store.DeleteCredential(ctx, "openai"); err != nil {
		t.Fatalf("delete credential: %v", err)
	}
	if err := store.DeleteCredential(ctx, "openai"); err != nil {
		t.Fatalf("idempotent delete credential: %v", err)
	}
	if _, found, err := store.LoadCredential(ctx, "openai"); err != nil || found {
		t.Fatalf("load deleted credential: found=%v err=%v", found, err)
	}
}

func TestKeyringStoreAcceptsLegacyOpaqueProviderReference(t *testing.T) {
	backend := &fakeKeyring{values: map[string]string{"provider/prv_test": "legacy-secret"}}
	store, err := newKeyringStore("CodePilot", backend)
	if err != nil {
		t.Fatalf("create Keyring store: %v", err)
	}
	value, found, err := store.LoadCredential(context.Background(), "provider/prv_test")
	if err != nil || !found || string(value) != "legacy-secret" {
		t.Fatalf("load legacy reference: value=%q found=%v err=%v", value, found, err)
	}
}

func TestKeyringErrorsAreClassifiedWithoutBackendDetails(t *testing.T) {
	backend := &fakeKeyring{values: make(map[string]string), getError: errors.New("backend exposed secret-value")}
	store, err := newKeyringStore("CodePilot Test", backend)
	if err != nil {
		t.Fatalf("create Keyring store: %v", err)
	}
	_, _, err = store.LoadCredential(context.Background(), "openai")
	if !errors.Is(err, provider.ErrCredentialStoreUnavailable) {
		t.Fatalf("expected unavailable classification, got %v", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("backend details leaked through error: %v", err)
	}
}

func TestChainStoreFallsBackToEnvironmentAndMutatesOnlyKeyring(t *testing.T) {
	backend := &fakeKeyring{values: make(map[string]string), getError: errors.New("unavailable")}
	primary, err := newKeyringStore("CodePilot Test", backend)
	if err != nil {
		t.Fatalf("create Keyring store: %v", err)
	}
	environment, err := newEnvironmentStore(map[string]string{"openai": "OPENAI_API_KEY"}, func(string) (string, bool) {
		return "environment-secret", true
	})
	if err != nil {
		t.Fatalf("create environment store: %v", err)
	}
	chain, err := NewChainStore(primary, environment)
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	value, found, err := chain.LoadCredential(context.Background(), "openai")
	if err != nil || !found || string(value) != "environment-secret" {
		t.Fatalf("fallback load: value=%q found=%v err=%v", value, found, err)
	}
	backend.getError = nil
	if err := chain.SaveCredential(context.Background(), "openai", provider.Credential("keyring-secret")); err != nil {
		t.Fatalf("save through chain: %v", err)
	}
	value, found, err = chain.LoadCredential(context.Background(), "openai")
	if err != nil || !found || string(value) != "keyring-secret" {
		t.Fatalf("primary load: value=%q found=%v err=%v", value, found, err)
	}
	if err := chain.DeleteCredential(context.Background(), "openai"); err != nil {
		t.Fatalf("delete through chain: %v", err)
	}
	value, found, err = chain.LoadCredential(context.Background(), "openai")
	if err != nil || !found || string(value) != "environment-secret" {
		t.Fatalf("fallback after delete: value=%q found=%v err=%v", value, found, err)
	}
}
