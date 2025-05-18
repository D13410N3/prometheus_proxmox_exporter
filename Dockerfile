# syntax=docker/dockerfile:1.4

FROM golang:1.23.2-alpine AS builder

WORKDIR /app
COPY . .

RUN go build -ldflags="-s -w" -o proxmox-exporter app.go

FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app/
COPY --from=builder /app/proxmox-exporter .

# Default port for the exporter
EXPOSE 9914

# Set up environment variables with default values
ENV PROXMOX_ADDRESS="127.0.0.1" \
    PROXMOX_PORT="8006" \
    LISTEN_ADDRESS="0.0.0.0:9914" \
    LOG_LEVEL="none"

ENTRYPOINT ["./proxmox-exporter"]
