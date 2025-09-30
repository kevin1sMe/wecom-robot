ARG GO_VERSION=1.23

FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src

# Optional tools; public modules do not require git when using proxy
RUN apk add --no-cache ca-certificates tzdata

# Ensure Go can automatically use the toolchain specified by go.mod
ENV GOTOOLCHAIN=auto

# Cache go mod separately for faster builds
COPY go.mod ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG CGO_ENABLED=0
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ENV CGO_ENABLED=${CGO_ENABLED}

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w" -o /out/wecom-robot ./cmd/wecom-robot


# Runtime image
FROM alpine:3.20

RUN addgroup -S app && adduser -S -G app app \
    && apk add --no-cache ca-certificates tzdata \
    && mkdir -p /app

WORKDIR /app
COPY --from=builder /out/wecom-robot /usr/local/bin/wecom-robot

# Default envs (can be overridden at runtime)
ENV PORT=8080

EXPOSE 8080
USER app
ENTRYPOINT ["/usr/local/bin/wecom-robot"]
