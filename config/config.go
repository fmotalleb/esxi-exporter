package config

import (
	"fmt"

	"github.com/fmotalleb/go-tools/defaulter"
	"github.com/mitchellh/mapstructure"
	"github.com/spf13/viper"
)

type Config struct {
	Hosts []ESXIHost `mapstructure:"hosts"`

	Secrets struct {
		Vault struct {
			Address string `mapstructure:"address"`
			Token   string `mapstructure:"token"`
			Path    string `mapstructure:"path"`
		} `mapstructure:"vault"`
		Bitwarden struct {
			ServerURL string `mapstructure:"server_url"`
			Email     string `mapstructure:"email"`
			Password  string `mapstructure:"password"`
			ItemID    string `mapstructure:"item_id"`
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

	// TODO: fetch secrets from vault / bitwarden if configured
	if cfg.Secrets.Vault.Address != "" {
		// placeholder: implement vault secret fetch
	}
	defaulter.ApplyDefaults(&cfg, nil)
	return &cfg, nil
}

type ESXIHost struct {
	Host     string `mapstructure:"host"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	Insecure bool   `mapstructure:"insecure"`
}
