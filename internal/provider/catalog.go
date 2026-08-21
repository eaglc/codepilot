package provider

import (
	"errors"
	"fmt"
	"net/http"
)

// Catalog stores explicitly registered provider adapters in stable order.
type Catalog struct {
	adapters map[Kind]Adapter
	kinds    []Kind
}

// NewCatalog validates and registers adapters without global init hooks.
func NewCatalog(adapters []Adapter) (*Catalog, error) {
	if len(adapters) == 0 {
		return nil, errors.New("create provider catalog: no adapters configured")
	}

	catalog := &Catalog{adapters: make(map[Kind]Adapter, len(adapters))}
	for _, adapter := range adapters {
		if adapter == nil {
			return nil, errors.New("create provider catalog: adapter is nil")
		}
		kind := adapter.Kind()
		if !kind.Valid() {
			return nil, fmt.Errorf("create provider catalog: unsupported kind %q", kind)
		}
		if _, exists := catalog.adapters[kind]; exists {
			return nil, fmt.Errorf("create provider catalog: duplicate kind %q", kind)
		}
		defaults := adapter.Defaults()
		if defaults.DisplayName == "" {
			return nil, fmt.Errorf("create provider catalog: invalid defaults for %q", kind)
		}
		if kind != KindOpenAICompatible && defaults.BaseURL == "" {
			return nil, fmt.Errorf("create provider catalog: default base URL is empty for %q", kind)
		}
		if kind != KindOpenAICompatible && defaults.ModelID == "" {
			return nil, fmt.Errorf("create provider catalog: default model is empty for %q", kind)
		}
		catalog.adapters[kind] = adapter
		catalog.kinds = append(catalog.kinds, kind)
	}

	return catalog, nil
}

// DefaultAdapters returns all MVP adapters in their picker order.
func DefaultAdapters(client *http.Client) []Adapter {
	return []Adapter{
		NewOpenAIAdapter(client),
		NewDeepSeekAdapter(client),
		NewOllamaAdapter(client),
		NewCompatibleAdapter(client),
	}
}

// Lookup returns the adapter for kind.
func (c *Catalog) Lookup(kind Kind) (Adapter, bool) {
	if c == nil {
		return nil, false
	}
	adapter, exists := c.adapters[kind]
	return adapter, exists
}

// Kinds returns a defensive copy of registration order.
func (c *Catalog) Kinds() []Kind {
	if c == nil {
		return nil
	}
	return append([]Kind(nil), c.kinds...)
}
