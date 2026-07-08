package config

import (
	"context"
	"sync"

	"github.com/fmotalleb/esxi-exporter/internal/secure"
)

// PasswordResolver resolves a password for a given host. Each backend
// (vault, bitwarden, inline) implements this interface so the config
// loader is oblivious to the provenance of the credential.
type PasswordResolver interface {
	// ResolvePassword returns the password for the given host spec. The
	// caller must call Destroy() on the returned SecureBytes when done.
	// If the resolver cannot fulfil the request it returns nil without
	// error (a different resolver or fallback may be tried).
	ResolvePassword(ctx context.Context, host *ESXIHost) (*secure.SecureBytes, error)
}

// SecretStore holds all configured PasswordResolver instances and a
// per-host cache of resolved passwords stored in zeroable memory.
// Fetching a password for the same host is idempotent within the
// lifetime of the store (the result is cached after the first call).
type SecretStore struct {
	resolvers []PasswordResolver

	mu    sync.Mutex
	cache map[string]*secure.SecureBytes // keyed by host URL
}

// NewSecretStore creates a store with the given resolvers. Resolvers are
// tried in registration order; the first non-nil result wins.
func NewSecretStore(resolvers ...PasswordResolver) *SecretStore {
	return &SecretStore{
		resolvers: resolvers,
		cache:     make(map[string]*secure.SecureBytes),
	}
}

// GetPassword returns the password for the given host. If the host has an
// inline password it is used directly. Otherwise each registered resolver
// is tried until one returns a non-nil result. The result is cached so
// subsequent calls are instant. The caller must NOT call Destroy() on the
// returned SecureBytes — ownership belongs to the cache. Call
// SecretStore.Destroy() to wipe all cached passwords at shutdown.
func (s *SecretStore) GetPassword(ctx context.Context, host *ESXIHost) (*secure.SecureBytes, error) {
	// Fast path: already cached.
	s.mu.Lock()
	if cached, ok := s.cache[host.Host]; ok {
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	// Inline password from config file – store it in secure memory.
	if host.Password.Len() > 0 {
		s.mu.Lock()
		// Double-check after acquiring write lock.
		if cached, ok := s.cache[host.Host]; ok {
			s.mu.Unlock()
			return cached, nil
		}
		// Make a fresh copy in zeroable memory.
		sec := secure.NewSecureBytesFromCopy(host.Password.Bytes())
		s.cache[host.Host] = sec
		s.mu.Unlock()
		return sec, nil
	}

	// Try each registered resolver.
	for _, r := range s.resolvers {
		pw, err := r.ResolvePassword(ctx, host)
		if err != nil {
			return nil, err
		}
		if pw != nil {
			s.mu.Lock()
			s.cache[host.Host] = pw
			s.mu.Unlock()
			return pw, nil
		}
	}

	return nil, nil // no password configured
}

// Destroy wipes all cached passwords from memory.
func (s *SecretStore) Destroy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sec := range s.cache {
		sec.Destroy()
	}
	s.cache = make(map[string]*secure.SecureBytes)
}
