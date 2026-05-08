# Multi-stage Dockerfile for the LiteMLflow operator.
#
# Same pattern as the main image — distroless static, pure-Go build.

FROM golang:1.26.3-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates

# The operator is its own Go module; copy + build from operator/ dir.
COPY operator/go.mod operator/go.sum ./
RUN go mod download

COPY operator/ ./
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/litemlflow-operator ./

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/litemlflow-operator /litemlflow-operator
USER nonroot:nonroot
EXPOSE 8080 8081
ENTRYPOINT ["/litemlflow-operator"]
