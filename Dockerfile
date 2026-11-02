# Build the panel first, so the binary embeds it.
FROM oven/bun:1 AS panel
WORKDIR /panel
COPY panel/ ./
RUN bun install --frozen-lockfile && bun run build

FROM golang:1.26-bookworm AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=panel /panel/dist/index.html ./internal/tpr/webui/dist/management.html

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG UPSTREAM_VERSION=unknown
ARG PANEL_VERSION=unknown

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}' -X 'github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/update.UpstreamVersion=${UPSTREAM_VERSION}' -X 'github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/update.PanelVersion=${PANEL_VERSION}'" \
    -o /usr/local/bin/tpr ./cmd/server/

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /usr/local/bin/tpr /usr/local/bin/tpr

# Everything lives in one folder. Mount a volume here to keep it.
ENV TPR_HOME=/data
# Loopback inside a container cannot be reached from outside. Publish the port on
# the host's loopback instead, as docker-compose.yml does.
ENV TPR_BIND_HOST=0.0.0.0
VOLUME /data
WORKDIR /data
EXPOSE 8317
CMD ["tpr"]
