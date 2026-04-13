.DEFAULT_GOAL := help

.PHONY: help pull up down dev build run stop clean logs

help:
	@echo "Webservice - Available Commands"
	@echo "================================"
	@echo ""
	@echo "Infrastructure:"
	@echo "  make pull       Pull required containers (postgres, redis)"
	@echo "  make up         Start infrastructure services"
	@echo "  make down       Stop infrastructure services"
	@echo "  make logs       View container logs"
	@echo ""
	@echo "Development:"
	@echo "  make dev        Run webservice locally (go run)"
	@echo "  make build      Build webservice binary"
	@echo "  make run        Run built binary"
	@echo ""
	@echo "Container:"
	@echo "  make build-image Build Docker image"
	@echo "  make run-container Run webservice in container"
	@echo "  make stop-container Stop webservice container"
	@echo ""
	@echo "Cleanup:"
	@echo "  make clean      Remove containers, volumes and build artifacts"
	@echo ""
	@echo "Ports:"
	@echo "  PostgreSQL: localhost:5433"
	@echo "  Redis:      localhost:6379"
	@echo "  Webservice: localhost:8082"

pull:
	@echo "Pulling required containers..."
	docker pull postgis/postgis:17-3.5
	docker pull redis:7-alpine
	@echo "✓ Containers pulled"

up:
	@echo "Starting infrastructure..."
	docker compose up -d postgres redis
	@echo ""
	@echo "✓ Services started:"
	@echo "  PostgreSQL: localhost:5433"
	@echo "  Redis:      localhost:6379"

down:
	docker compose down

logs:
	docker compose logs -f

dev:
	@echo "Starting webservice..."
	go run main.go

build:
	@echo "Building webservice..."
	go build -o main .
	@echo "✓ Built: ./main"

run: build
	./main

build-image:
	@echo "Building Docker image..."
	docker build -t webservice:local .
	@echo "✓ Image built: webservice:local"

run-container:
	@echo "Running webservice container..."
	docker run -d --name webservice --network host -p 8082:8082 --env-file .env webservice:local
	@echo "✓ Webservice running on localhost:8082"

stop-container:
	docker stop webservice || true
	docker rm webservice || true

clean:
	docker compose down -v
	rm -f main
	@echo "✓ Cleaned"
