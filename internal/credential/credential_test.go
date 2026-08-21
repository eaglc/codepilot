package credential

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eaglc/codepilot/internal/provider"
	"github.com/eaglc/codepilot/internal/session"
	keyring "github.com/zalando/go-keyring"
)

func TestMemoryStore_CopiesCredentialsAndClearsOnClose(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	input := provider.Secret("test-secret")

	location, err := store.Put(ctx, "prv_test", input)
	if err != nil {
		t.Fatalf("put credential: %v", err)
	}
	if location != provider.CredentialInMemory {
		t.Fatalf("location = %s, want memory", location)
	}
	input[0] = 'X'

	first, found, err := store.Get(ctx, "prv_test")
	if err != nil || !found || string(first) != "test-secret" {
		t.Fatalf("get credential = %q, %t, %v", first, found, err)
	}
	first[0] = 'Y'
	second, found, err := store.Get(ctx, "prv_test")
	if err != nil || !found || string(second) != "test-secret" {
		t.Fatalf("get copied credential = %q, %t, %v", second, found, err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close credential store: %v", err)
	}
	if _, _, err := store.Get(ctx, "prv_test"); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("get after close error = %v, want closed", err)
	}
}

func TestFallbackStore_UsesMemoryOnlyForUnavailableKeyring(t *testing.T) {
	ctx := context.Background()
	unavailable := errors.New("backend unavailable")
	keyringStore := &KeyringStore{backend: &fakeKeyringBackend{setErr: unavailable, getErr: unavailable}}
	memoryStore := NewMemoryStore()
	store := NewFallbackStore(keyringStore, memoryStore)

	location, err := store.Put(ctx, "prv_test", provider.Secret("test-secret"))
	if err != nil {
		t.Fatalf("fallback put: %v", err)
	}
	if location != provider.CredentialInMemory {
		t.Fatalf("location = %s, want memory", location)
	}
	secret, found, err := store.Get(ctx, "prv_test")
	if err != nil || !found || string(secret) != "test-secret" {
		t.Fatalf("fallback get = %q, %t, %v", secret, found, err)
	}

	tooLargeBackend := &fakeKeyringBackend{setErr: keyring.ErrSetDataTooBig}
	nonFallbackMemory := NewMemoryStore()
	nonFallback := NewFallbackStore(&KeyringStore{backend: tooLargeBackend}, nonFallbackMemory)
	if _, err := nonFallback.Put(ctx, "prv_test", provider.Secret("test-secret")); !errors.Is(err, keyring.ErrSetDataTooBig) {
		t.Fatalf("non-availability error = %v, want keyring error", err)
	}
	if _, found, err := nonFallbackMemory.Get(ctx, "prv_test"); err != nil || found {
		t.Fatalf("non-availability error populated memory: found=%t err=%v", found, err)
	}
}

func TestKeyringStore_UsesStableAccountAndSafeErrors(t *testing.T) {
	backend := &fakeKeyringBackend{getErr: errors.New("backend mentions sk-test-secret")}
	store := &KeyringStore{backend: backend}

	_, _, err := store.Get(context.Background(), "prv_test")
	if !errors.Is(err, ErrKeyringUnavailable) {
		t.Fatalf("get error = %v, want unavailable", err)
	}
	if strings.Contains(err.Error(), "sk-test-secret") {
		t.Fatalf("keyring error exposed backend detail: %v", err)
	}
	if backend.lastService != "CodePilot" || backend.lastAccount != "provider/prv_test" {
		t.Fatalf("keyring target = %q/%q", backend.lastService, backend.lastAccount)
	}
}

func TestFallbackStore_DeleteClearsMemoryAndReportsKeyringFailure(t *testing.T) {
	ctx := context.Background()
	memoryStore := NewMemoryStore()
	if _, err := memoryStore.Put(ctx, "prv_test", provider.Secret("test-secret")); err != nil {
		t.Fatalf("seed memory credential: %v", err)
	}
	backend := &fakeKeyringBackend{deleteErr: errors.New("unavailable")}
	store := NewFallbackStore(&KeyringStore{backend: backend}, memoryStore)

	err := store.Delete(ctx, session.ProviderProfileID("prv_test"))
	if !errors.Is(err, ErrKeyringUnavailable) {
		t.Fatalf("delete error = %v, want unavailable warning", err)
	}
	if _, found, err := memoryStore.Get(ctx, "prv_test"); err != nil || found {
		t.Fatalf("memory credential remains after delete: found=%t err=%v", found, err)
	}
}

type fakeKeyringBackend struct {
	secret       string
	getErr       error
	setErr       error
	deleteErr    error
	lastService  string
	lastAccount  string
	lastPassword string
}

func (b *fakeKeyringBackend) Get(service string, account string) (string, error) {
	b.lastService = service
	b.lastAccount = account
	return b.secret, b.getErr
}

func (b *fakeKeyringBackend) Set(service string, account string, secret string) error {
	b.lastService = service
	b.lastAccount = account
	b.lastPassword = secret
	return b.setErr
}

func (b *fakeKeyringBackend) Delete(service string, account string) error {
	b.lastService = service
	b.lastAccount = account
	return b.deleteErr
}
