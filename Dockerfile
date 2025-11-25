# syntax=docker/dockerfile:1

FROM golang:1.22-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /build/reelser-bot ./cmd/bot

FROM debian:bookworm-slim AS runtime

ENV APP_HOME=/app \
    TEMP_DIR=/app/tmp \
    MAX_VIDEO_SIZE_MB=50 \
    VIDEO_QUALITY=best \
    WORKER_POOL_SIZE=4

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates python3 python3-pip && \
    pip3 install --no-cache-dir --break-system-packages yt-dlp && \
    rm -rf /var/lib/apt/lists/*

WORKDIR ${APP_HOME}

COPY --from=builder /build/reelser-bot /usr/local/bin/reelser-bot

RUN mkdir -p ${TEMP_DIR}

VOLUME ["${TEMP_DIR}"]

ENTRYPOINT ["reelser-bot"]

