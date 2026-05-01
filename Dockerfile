################################################################################
# Build stage — needs CGo for github.com/mattn/go-sqlite3
################################################################################
FROM golang:1.25-bookworm AS builder

ENV CGO_ENABLED=1 \
    GOOS=linux \
    GOFLAGS=-buildvcs=false

# go-sqlite3 brings its own SQLite via cgo, but sqlite-vec-go-bindings/cgo
# includes the system <sqlite3.h> header — install libsqlite3-dev so the
# vec extension cgo bridge compiles.
RUN apt-get update \
 && apt-get install -y --no-install-recommends libsqlite3-dev \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# Cache deps before copying source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=docker
RUN go build -trimpath -ldflags "-X main.Version=${VERSION}" -o /out/everychat ./cmd/everychat \
 && go build -trimpath -ldflags "-X main.Version=${VERSION}" -o /out/everychat-ops ./cmd/everychat-ops

################################################################################
# Runtime stage — slim debian (CGo / glibc), non-root user
################################################################################
FROM debian:bookworm-slim AS runtime

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tzdata \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --create-home --uid 10001 --gid users everychat

WORKDIR /app
COPY --from=builder /out/everychat     /app/everychat
COPY --from=builder /out/everychat-ops /app/everychat-ops

RUN mkdir -p /app/data && chown -R everychat:users /app

USER everychat
EXPOSE 8080
ENV EVERYCHAT_ADDR=":8080" \
    EVERYCHAT_DB_PATH="/app/data/everychat.db"

ENTRYPOINT ["/app/everychat"]
