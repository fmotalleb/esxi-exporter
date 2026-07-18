# esxi-exporter

Prometheus exporter for VMware ESXi (all metrics + per-VM + cluster).

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
hosts:
  - host: "https://vcsa-host.idc.local/sdk/"
    username: "username"
    password: "password"
    insecure: true
  - host: "https://vcsa-host2.idc.local/sdk/"
    username: "username"
    password: "password"
    insecure: true

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
