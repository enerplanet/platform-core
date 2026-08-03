# SpatialHub Webservice

`platform-core/webservice` is a standalone Go microservice (Gin + GORM + Asynq + Redis) that sits between the EnerPlanET backend and the simulation workers (Calliope / PyPSA). It has three responsibilities:

### 1. Instance management (REST API)

It owns the `WebserviceInstance` table and exposes a full CRUD + operations API:

| Endpoint                                                      | Purpose                                                 |
| ------------------------------------------------------------- | ------------------------------------------------------- |
| `POST /api/webservices`                                       | Register a compute instance                             |
| `GET /api/webservices`                                        | List instances (filter by status/available/busy/search) |
| `GET /api/webservices/:id`                                    | Get one instance                                        |
| `PUT /api/webservices/:id`                                    | Update (incl. `auto_scaling`, `max_concurrency`)        |
| `DELETE /api/webservices/:id`                                 | Delete an instance                                      |
| `POST /api/webservices/:id/{available,unavailable,busy,idle}` | Manual state changes                                    |
| `POST /api/webservices/:id/heartbeat`                         | Update `last_heartbeat`                                 |
| `GET /api/webservices/:id/health`                             | Health check (HTTP + TCP fallback)                      |
| `GET /api/webservices/:id/ping`                               | Ping + return details                                   |
| `POST /api/webservices/:id/request`                           | Send arbitrary JSON to an instance                      |
| `GET /api/webservices/summary`                                | Total / active / available counts                       |
| `GET /api/webservices/available-static-dates`                 | Proxy to an instance                                    |
| `GET /api/webservices/available-data-coverage`                | Proxy to an instance                                    |
| `POST /api/internal/webservices/:id/release`                  | Decrement concurrency (called by backend)               |
| `POST /api/internal/webservices/:id/cancel-session`           | Cancel a running session (called by backend)            |

### 2. Dispatch worker (Asynq consumer)

It consumes the `dispatch_model_calculation` task that the backend enqueues into the `spatialAI_public` Redis queue. For each task it:

1. **Reserves** an available instance atomically (row-lock, respects `max_concurrency` and CPU threshold).
2. **Claims** the model via the backend's internal lifecycle API (`POST /api/internal/models/:id/mark-running`); releases the instance if the race was lost.
3. **Forwards** the calculation payload to the instance (`/calliope/start` by default) and persists session metadata back to the backend.

Worker concurrency is derived from the sum of active instance capacity (`auto_scaling` + `max_concurrency`), bounded between a CPU fallback and 512.

### 3. Scheduler

A background ticker (default 30s) that:

- **Pings** all instances, updates online/offline status, and records CPU/memory usage.
- **Fails models** whose compute instance went offline.
- **Fixes stuck concurrency** counters against the backend's active-model count.
- **Fails stuck models** that exceed the running timeout (default 720 min).

## Network communication

The webservice listens on and connects to the following addresses and ports:

### Listens on (inbound)

| Address                                   | Port               | Purpose                                                         |
| ----------------------------------------- | ------------------ | --------------------------------------------------------------- |
| `WEBSERVICE_APP_HOST:WEBSERVICE_APP_PORT` | **8082** (default) | REST API for instance management + internal lifecycle endpoints |
| Default: `0.0.0.0:8082`                   |                    |                                                                 |

### Connects to (outbound)

| Target                           | Default address                 | Port                             | Purpose                                                                   |
| -------------------------------- | ------------------------------- | -------------------------------- | ------------------------------------------------------------------------- |
| **PostgreSQL**                   | `DB_HOST:DB_PORT`               | **5433** (dev) / **5432** (prod) | Read/write `WebserviceInstance` table, derive dispatch concurrency        |
| **Redis**                        | `REDIS_HOST:REDIS_PORT`         | **6379**                         | Asynq task queue (`spatialAI_public`), heartbeat/status coordination      |
| **Backend lifecycle API**        | `BACKEND_INTERNAL_URL`          | **8000**                         | `POST /api/internal/models/:id/mark-running` — claim a model              |
|                                  |                                 |                                  | `POST /api/internal/models/:id/mark-failed` — report failure              |
|                                  |                                 |                                  | `PATCH /api/internal/models/:id/run-session` — persist session metadata   |
|                                  |                                 |                                  | `GET /api/internal/models/active` — list active models for reconciliation |
| **Simulation workers** (dynamic) | From `WebserviceInstance` table | **varies**                       | `GET /health` — health check (scheduler)                                  |
|                                  | e.g. `sim-haproxy:8089`         |                                  | `GET /status` — fetch CPU/memory usage                                    |
|                                  |                                 |                                  | `POST /calliope/start` — dispatch calculation (default endpoint)          |
|                                  |                                 |                                  | `DELETE /cancel/{sessionID}` — cancel a running session                   |
|                                  |                                 |                                  | `GET /available-static-dates` — proxy                                     |
|                                  |                                 |                                  | `GET /available-data-coverage` — proxy                                    |

### Environment variable defaults

| Variable               | Default (dev)           | Default (production)         |
| ---------------------- | ----------------------- | ---------------------------- |
| `WEBSERVICE_APP_HOST`  | `0.0.0.0`               | `0.0.0.0`                    |
| `WEBSERVICE_APP_PORT`  | `8082`                  | `8002`                       |
| `DB_HOST`              | `localhost`             | `postgres`                   |
| `DB_PORT`              | `5433`                  | `5432`                       |
| `REDIS_HOST`           | `localhost`             | `redis`                      |
| `REDIS_PORT`           | `6379`                  | `6379`                       |
| `BACKEND_INTERNAL_URL` | `http://localhost:8000` | `http://app-backend:8000`    |
| `CALLBACK_URL`         | `http://localhost:8000` | `https://wildfire.th-deg.de` |

## Local development

```bash
make up        # start postgres + redis
make dev       # run webservice locally (go run)
make build     # build binary
```

Requires a running backend (for the internal lifecycle API) and a reachable simulation worker.
