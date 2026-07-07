FROM scratch
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/esxi-exporter /
ENTRYPOINT ["/esxi-exporter"]
