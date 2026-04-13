# Platform Core

Core infrastructure and microservices for the Platform.

## Services

- **Auth Service**: Identity and access management (Keycloak-based).
- **Webservice**: Main API and job orchestration.
- **GeoServer**: Spatial data management and proxying.

## Development

### Prerequisites

- Docker & Docker Compose
- Go 1.22+

### Quick Start

Each service contains a `Makefile` for local development.

1. **Infrastructure**:

   ```bash
   docker compose up -d
   ```

2. **Run Services**:
   Navigate to a service directory and run:
   ```bash
   make dev
   ```

### Common Commands

| Command      | Description                                     |
| ------------ | ----------------------------------------------- |
| `make up`    | Start required infrastructure (DB, Redis, etc.) |
| `make dev`   | Run service in development mode                 |
| `make build` | Build service binary                            |
| `make clean` | Remove containers and build artifacts           |

## Infrastructure Ports

- **PostgreSQL**: 5433
- **Redis**: 6379
- **Keycloak**: 8080
- **Auth-Service**: 8081
- **Webservice**: 8082
- **GeoService**: 8083
- **GeoServer**: 8180
