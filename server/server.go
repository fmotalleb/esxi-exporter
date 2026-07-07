package server

import (
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/exporter-toolkit/web"

	"github.com/fmotalleb/esxi-exporter/collector"
	"github.com/fmotalleb/esxi-exporter/config"
)

func Run(cfg *config.Config) error {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collector.NewESXiCollector(cfg))

	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

	mux := http.NewServeMux()
	mux.Handle("/metrics", handler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
			<head><title>ESXi Exporter</title></head>
			<body><h1>ESXi Exporter</h1>
			<p><a href="/metrics">Metrics</a></p>
			</body></html>`))
	})

	listen := ":9272"
	if cfg.Web.ListenAddress != "" {
		listen = cfg.Web.ListenAddress
	}

	server := &http.Server{Addr: listen, Handler: mux}

	if cfg.Web.TLS.CertFile != "" && cfg.Web.TLS.KeyFile != "" {
		log.Printf("Listening on %s (TLS)", listen)
		return web.ListenAndServe(server, &web.FlagConfig{
			WebListenAddresses: &[]string{listen},
			WebSystemdSocket:   new(bool),
			WebConfigFile:      nil,
		}, nil)
	}

	log.Printf("Listening on %s", listen)
	return server.ListenAndServe()
}
