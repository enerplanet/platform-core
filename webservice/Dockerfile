# Build the webservice binary
FROM golang:1.24-alpine AS builder

WORKDIR /build
ENV GOTOOLCHAIN=auto

RUN apk add --no-cache build-base git ca-certificates

# Copy infrastructure (common and platform)
COPY infrastructure/common /build/infrastructure/common
COPY infrastructure/platform /build/infrastructure/platform

# Copy webservice
COPY platform-core/webservice /build/webservice

WORKDIR /build/webservice

# Fix go.mod paths for Docker build (since we copied infrastructure to /build/infrastructure)
RUN sed -i 's|../../infrastructure|../infrastructure|g' go.mod

# Download dependencies and tidy
RUN go mod tidy && go mod download

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /build/bin/webservice ./cmd/main.go

# Minimal runtime image
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 appuser

WORKDIR /app

COPY --from=builder /build/bin/webservice /app/webservice
COPY --from=builder /build/webservice/.env.example /app/.env
RUN mkdir -p /app/logs && chown -R appuser:appuser /app

USER appuser

ENV WEBSERVICE_APP_HOST=0.0.0.0 WEBSERVICE_APP_PORT=8002
EXPOSE 8002

ENTRYPOINT ["/app/webservice"]
