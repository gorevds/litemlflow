# Multi-stage Dockerfile for LiteMLflow.
#
# The final image is from gcr.io/distroless/static — no shell, no package
# manager, just the binary. The image is reproducible-friendly: the build
# stage uses the Go module proxy.

FROM golang:1.26.4-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates

# Cache deps separately from source for fast incremental rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w \
      -X github.com/gorevds/litemlflow/pkg/version.Version=${VERSION} \
      -X github.com/gorevds/litemlflow/pkg/version.Commit=${COMMIT} \
      -X github.com/gorevds/litemlflow/pkg/version.Date=${DATE}" \
    -o /out/litemlflow \
    ./cmd/litemlflow
# Empty data dir, copied below with nonroot ownership: an anonymous volume
# inherits the image directory's owner, and a root-owned /data made the
# nonroot server fail with "unable to open database file".
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/litemlflow /litemlflow
COPY --from=build --chown=65532:65532 /out/data /data
EXPOSE 5000
VOLUME ["/data"]
ENV LITEMLFLOW_DATA=/data \
    LITEMLFLOW_ADDR=:5000
USER nonroot:nonroot
# Exec form: distroless has no shell/curl. The healthcheck subcommand derives
# its URL from LITEMLFLOW_ADDR (default http://127.0.0.1:5000/healthz).
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/litemlflow", "healthcheck"]
ENTRYPOINT ["/litemlflow"]
CMD ["up"]
