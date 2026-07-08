package config

import (
	"context"
	"fmt"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/fmotalleb/esxi-exporter/internal/secure"
)

// VaultResolver fetches passwords from HashiCorp Vault's KV v2 secrets
// engine. Each host can specify a vault_path that points to the secret.
type VaultResolver struct {
	client    *vaultapi.Client
	mountPath string
}

// NewVaultResolver creates a resolver that reads secrets from the given
// Vault mount path using the provided client config (address + token).
func NewVaultResolver(address, token, mountPath string) (*VaultResolver, error) {
	cfg := vaultapi.DefaultConfig()
	cfg.Address = address

	client, err := vaultapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("vault: create client: %w", err)
	}
	client.SetToken(token)

	return &VaultResolver{
		client:    client,
		mountPath: mountPath,
	}, nil
}

// ResolvePassword reads the secret at host.VaultPath from the KV v2
// engine and returns the value of the "password" key. Returns nil if the
// host has no vault_path configured.
func (v *VaultResolver) ResolvePassword(ctx context.Context, host *ESXIHost) (*secure.SecureBytes, error) {
	if host.VaultPath == "" {
		return nil, nil // not for us
	}

	secret, err := v.client.KVv2(v.mountPath).Get(ctx, host.VaultPath)
	if err != nil {
		return nil, fmt.Errorf("vault: read %s: %w", host.VaultPath, err)
	}
	if secret == nil || secret.Data == nil {
		return nil, fmt.Errorf("vault: secret %s is empty", host.VaultPath)
	}

	pwRaw, ok := secret.Data["password"]
	if !ok {
		return nil, fmt.Errorf("vault: secret %s has no 'password' key", host.VaultPath)
	}

	pwStr, ok := pwRaw.(string)
	if !ok {
		return nil, fmt.Errorf("vault: secret %s 'password' is not a string", host.VaultPath)
	}

	return secure.NewSecureBytes(pwStr), nil
}
