FROM golang:1 AS builder
WORKDIR /build
COPY . .
RUN set -eux ; \
  apt-get update; \
  apt-get install -y --no-install-recommends ca-certificates; \
  go mod download; \
  go mod tidy; \
  mkdir -p output; \
  mkdir -p config; \
  CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-extldflags "-static"' -o k8s-node-external-ip-watcher .

FROM gcr.io/distroless/static:nonroot AS runtime
WORKDIR /app
COPY --from=builder --chown=nonroot:nonroot /build/config /config
COPY --from=builder --chown=nonroot:nonroot /build/output /output
COPY --from=builder --chown=nonroot:nonroot /build/k8s-node-external-ip-watcher .
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ENTRYPOINT ["/app/k8s-node-external-ip-watcher"]
CMD ["--config", "/config/config.yaml"]
