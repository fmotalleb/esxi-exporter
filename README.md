# esxi-exporter

Prometheus exporter for VMware ESXi (all metrics + per-VM).

## Usage

```bash
esxi-exporter --config /path/to/config.yaml
```

## Configuration

Supports YAML config with `mapstructure` tags. Secrets can be read from:

- Raw values
- HashiCorp Vault
- Bitwarden-compatible backends

Web config is fully compatible with `web.yml` used by Prometheus node_exporter.

Example `config.yaml`:

```yaml
esxi:
  host: "https://esxi.example.com"
  username: "root"
  password: "secret"

web:
  listen_address: ":9272"
  tls:
    cert_file: "/etc/ssl/cert.pem"
    key_file: "/etc/ssl/key.pem"
```

## Build

```bash
go mod tidy
go build ./cmd/esxi-exporter
```

## Metrics

The exporter collects:
- Host: CPU, Memory, Disk, Network
- Per-VM: CPU, Memory, Disk, Network (all possible metrics)