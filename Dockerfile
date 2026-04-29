# syntax=docker/dockerfile:1.7

FROM golang:1.24-bookworm AS builder

WORKDIR /src

RUN apt-get update \
    && apt-get install -y --no-install-recommends build-essential ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/cyberstrike-ai ./cmd/server/main.go

# ── gs-netcat download stage ─────────────────────────────────────────────────
FROM debian:bookworm-slim AS gsnetcat-downloader

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl python3 \
    && rm -rf /var/lib/apt/lists/*

RUN set -e; \
    ASSET="gs-netcat_linux-x86_64"; \
    URL=$(curl -sf https://api.github.com/repos/hackerschoice/gsocket/releases/latest \
          | python3 -c "import sys,json; print(next(a['browser_download_url'] for a in json.load(sys.stdin)['assets'] if a['name']=='${ASSET}'))"); \
    curl -sfL "$URL" -o /usr/local/bin/gs-netcat; \
    chmod +x /usr/local/bin/gs-netcat

# ── runtime image ─────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       ca-certificates tzdata bash \
       python3 python3-pip \
    && pip3 install --break-system-packages --no-cache-dir mcp>=1.0.0 httpx>=0.27.0 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=gsnetcat-downloader /usr/local/bin/gs-netcat /usr/local/bin/gs-netcat

COPY --from=builder /out/cyberstrike-ai /app/cyberstrike-ai
COPY web /app/web
COPY tools /app/tools
COPY skills /app/skills
COPY roles /app/roles
COPY knowledge_base /app/knowledge_base
COPY scripts /app/scripts
COPY containers/panel-mcp /app/containers/panel-mcp
COPY requirements.txt /app/requirements.txt
COPY run_docker.sh /app/run_docker.sh
COPY config.docker.yaml /app/config.docker.yaml

RUN chmod +x /app/scripts/install-enabled-tools-container.sh \
    && chmod +x /app/run_docker.sh \
    && /app/scripts/install-enabled-tools-container.sh \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /app/data /app/tmp \
    && chmod +x /app/cyberstrike-ai

EXPOSE 8080 8081

ENV CYBERSTRIKE_DOCKER=true \
    GS_NETCAT_BIN=/usr/local/bin/gs-netcat

ENTRYPOINT ["/app/cyberstrike-ai", "--config", "/app/config.docker.yaml"]
