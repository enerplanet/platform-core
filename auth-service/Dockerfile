FROM golang:1.24-alpine AS builder

WORKDIR /build
ENV GOTOOLCHAIN=auto

RUN apk add --no-cache build-base ca-certificates git

# Copy infrastructure (common and platform)
COPY infrastructure/common /build/infrastructure/common
COPY infrastructure/platform /build/infrastructure/platform

# Copy auth-service
COPY platform-core/auth-service /build/auth-service

WORKDIR /build/auth-service

# Fix go.mod paths for Docker build (since we copied infrastructure to /build/infrastructure)
RUN sed -i 's|../../infrastructure|../infrastructure|g' go.mod

# Download dependencies and tidy
RUN go mod tidy && go mod download

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /build/bin/auth-service ./cmd/main.go

FROM ubuntu:22.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata curl && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /build/bin/auth-service /app/auth-service
COPY --from=builder /build/auth-service/internal/handler/auth/templates /app/internal/handler/auth/templates
COPY --from=builder /build/auth-service/.env.example /app/.env

RUN mkdir -p /app/logs && chmod +x /app/auth-service

EXPOSE 8001
ENV APP_HOST=0.0.0.0 APP_PORT=8001

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD curl -f http://localhost:8001/api/health || exit 1

ENTRYPOINT ["/app/auth-service"]
