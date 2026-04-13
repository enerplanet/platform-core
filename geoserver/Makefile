.DEFAULT_GOAL := help

.PHONY: help pull up down dev build run stop clean logs build-image run-container stop-container

help:
	@echo "GeoServer Service - Available Commands"
	@echo "======================================="
	@echo ""
	@echo "Infrastructure:"
	@echo "  make pull       Pull required containers (postgres, geoserver)"
	@echo "  make up         Start infrastructure services"
	@echo "  make down       Stop infrastructure services"
	@echo "  make logs       View container logs"
	@echo ""
	@echo "Development:"
	@echo "  make dev        Run geoservice locally (go run)"
	@echo "  make build      Build geoservice binary"
	@echo "  make run        Run built binary"
	@echo ""
	@echo "Container:"
	@echo "  make build-image Build Docker image"
	@echo "  make run-container Run geoservice in container"
	@echo "  make stop-container Stop geoservice container"
	@echo ""
	@echo "Cleanup:"
	@echo "  make clean      Remove containers, volumes and build artifacts"
	@echo ""
	@echo "Ports:"
	@echo "  PostgreSQL: localhost:5433"
	@echo "  GeoServer:  localhost:8180"
	@echo "  GeoService: localhost:8083"

pull:
	@echo "Pulling required containers..."
	docker pull postgis/postgis:17-3.5
	docker pull kartoza/geoserver:2.25.2
	@echo "✓ Containers pulled"

up:
	@echo "Starting infrastructure..."
	docker compose up -d postgres geoserver
	@echo ""
	@echo "✓ Services started:"
	@echo "  PostgreSQL: localhost:5433"
	@echo "  GeoServer:  http://localhost:8180/geoserver"

down:
	docker compose down

logs:
	docker compose logs -f

dev:
	@echo "Starting geoservice..."
	go run main.go

build:
	@echo "Building geoservice..."
	go build -o main .
	@echo "✓ Built: ./main"

run: build
	./main

build-image:
	@echo "Building Docker image..."
	docker build -t geoservice:local .
	@echo "✓ Image built: geoservice:local"

run-container:
	@echo "Running geoservice container..."
	docker run -d --name geoservice --network host -p 8083:8083 --env-file .env geoservice:local
	@echo "✓ Geoservice running on localhost:8083"

stop-container:
	docker stop geoservice || true
	docker rm geoservice || true

clean:
	docker compose down -v
	rm -f main
	@echo "✓ Cleaned"
