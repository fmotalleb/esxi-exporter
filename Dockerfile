FROM scratch
COPY esxi-exporter /
ENTRYPOINT ["/esxi-exporter"]
