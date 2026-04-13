# GeoServer control-plane microservice
FROM golang:1.24-alpine AS builder

WORKDIR /build
ENV GOTOOLCHAIN=auto

RUN apk add --no-cache build-base ca-certificates git

# Copy infrastructure (common and platform)
COPY infrastructure/common /build/infrastructure/common
COPY infrastructure/platform /build/infrastructure/platform

# Copy geoserver
COPY platform-core/geoserver /build/geoserver

WORKDIR /build/geoserver

# Fix go.mod paths for Docker build (since we copied infrastructure to /build/infrastructure)
RUN sed -i 's|../../infrastructure|../infrastructure|g' go.mod

# Download dependencies and tidy
RUN go mod tidy && go mod download

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /build/bin/geoserver-service ./cmd/main.go

FROM alpine:3.20
WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata && mkdir -p /app/logs

COPY --from=builder /build/bin/geoserver-service /app/server
COPY --from=builder /build/geoserver/.env.example /app/.env

ENV GEOSERVER_APP_HOST=0.0.0.0
EXPOSE 8083

ENTRYPOINT ["/app/server"]
