package config

import (
	"context"
	"fmt"

	"github.com/fmotalleb/go-tools/defaulter"
	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"

	"github.com/fmotalleb/esxi-exporter/internal/secure"
)

type Config struct {
	Hosts []ESXIHost `mapstructure:"hosts"`

	Secrets struct {
		Vault struct {
			Address   string `mapstructure:"address"`
			Token     string `mapstructure:"token"`
			MountPath string `mapstructure:"path"`
		} `mapstructure:"vault"`
		Bitwarden struct {
			ServerURL    string `mapstructure:"server_url"`
			SessionToken string `mapstructure:"session_token"`
		} `mapstructure:"bitwarden"`
	} `mapstructure:"secrets"`

	Web struct {
		ListenAddress string `mapstructure:"listen_address"`
		TLS           struct {
			CertFile string `mapstructure:"cert_file"`
			KeyFile  string `mapstructure:"key_file"`
		} `mapstructure:"tls"`
	} `mapstructure:"web"`

	Metrics MetricsConfig `mapstructure:"metrics"`

	// SecretStore is initialised after Load() returns. It holds cached
	// passwords in zeroable memory and is used by the collector instead
	// of reading ESXIHost.Password directly. Load returns a store only
	// when at least one resolver is configured; otherwise it remains nil
	// and the collector falls back to the inline password.
	SecretStore *SecretStore `mapstructure:"-"`
}

func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigType("yaml")

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName(".esxi-exporter")
		v.AddConfigPath("$HOME")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	decoder, _ := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:  &cfg,
		TagName: "mapstructure",
	})
	if err := decoder.Decode(v.AllSettings()); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	if err := cfg.initSecretStore(context.Background()); err != nil {
		return nil, fmt.Errorf("init secret store: %w", err)
	}

	defaulter.ApplyDefaults(&cfg, nil)
	return &cfg, nil
}

// initSecretStore builds the SecretStore by registering resolvers for any
// configured secret backends. Must be called after the config is decoded.
// The SecretStore is always created (even with zero resolvers) so that
// inline passwords in ESXIHost.Password are also cached in zeroable memory.
func (cfg *Config) initSecretStore(ctx context.Context) error {
	var resolvers []PasswordResolver

	if cfg.Secrets.Vault.Address != "" {
		mountPath := cfg.Secrets.Vault.MountPath
		if mountPath == "" {
			mountPath = "secret"
		}
		vr, err := NewVaultResolver(cfg.Secrets.Vault.Address, cfg.Secrets.Vault.Token, mountPath)
		if err != nil {
			return fmt.Errorf("vault resolver: %w", err)
		}
		resolvers = append(resolvers, vr)
	}

	if cfg.Secrets.Bitwarden.SessionToken != "" || cfg.Secrets.Bitwarden.ServerURL != "" {
		resolvers = append(resolvers, NewBitwardenResolver(cfg.Secrets.Bitwarden.ServerURL, cfg.Secrets.Bitwarden.SessionToken))
	}

	cfg.SecretStore = NewSecretStore(resolvers...)
	return nil
}

type ESXIHost struct {
	Host     string `mapstructure:"host"`
	Username string `mapstructure:"username"`
	Password secure.SafePassword `mapstructure:"password"`
	Insecure bool   `mapstructure:"insecure"`

	// VaultPath is the path of the secret in Vault's KV v2 engine
	// (e.g. "databases/esxi/prod"). When set, the Vault resolver will
	// fetch the password from this path instead of using the inline
	// password field.
	VaultPath string `mapstructure:"vault_path"`

	// BitwardenItemID is the ID of a Bitwarden item whose
	// login.password field contains the ESXi password.
	BitwardenItemID string `mapstructure:"bitwarden_item_id"`
}
