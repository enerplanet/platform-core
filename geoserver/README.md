# GeoServer Service

`platform-core/geoserver` is a Go control-plane microservice (Gin + GORM) that manages the GeoServer instance used to serve fire-risk raster layers for the EnerPlanET platform. It provisions GeoServer resources (workspaces, coverage stores, layers, SLD styles) for model results, proxies WMS requests to the frontend, and samples raster pixels to produce risk distributions and geo-grid data for charts and heatmaps.

## What it does

The service is the bridge between the platform's PostgreSQL database and a GeoServer instance. It:

- **Provisions layers** — turns a completed model result (a GeoTIFF) into a published GeoServer coverage layer with fire-risk styling.
- **Manages the lifecycle** — creates and deletes coverage stores and layers as results are configured or removed.
- **Applies risk styles** — upserts a set of SLD styles that classify fire risk into 5 levels (very low → very high) plus contextual overlays (moderate+, high+, individual levels).
- **Serves WMS to the frontend** — proxies WMS requests through a public endpoint so the browser never talks to GeoServer directly.
- **Samples raster data** — reads pixel values via WMS `GetFeatureInfo` to compute risk distributions and positioned grid samples for frontend charts and heatmaps.
- **Reports layer bounds** — returns the cached bounding box of a published layer.

## Architecture

```
┌──────────────┐   /api/geoserver-proxy/:ws/wms   ┌──────────────────┐   REST   ┌────────────┐
│  Frontend    │──────────────────────────────────▶│  GeoServer       │─────────▶│  GeoServer │
│  (React)     │                                   │  Service (:8083) │          │  (:8180)   │
└──────────────┘                                   └────────┬─────────┘          └────────────┘
                                                            │
                                            /api/internal/geoserver/*
                                                            │
                                                            ▼
                                                     ┌──────────────┐
                                                     │  PostgreSQL  │
                                                     │  (results)   │
                                                     └──────────────┘
```

The service is a thin Go control plane. It reads model results from the shared PostgreSQL database, then drives GeoServer's REST API to create and style layers. GeoServer itself runs as a separate container (`kartoza/geoserver:2.25.2`) with the result GeoTIFFs mounted into its data directory.

## How it works

### Layer provisioning

When the backend marks a model result as extracted, it calls `POST /api/internal/geoserver/results/:id/configure`. The service then:

1. Loads the `ModelResult` from the database and verifies extraction is complete.
2. Sets the result's `geoserver_status` to `processing`.
3. Ensures the `fire_risk` workspace exists.
4. Ensures the fire-risk SLD styles are provisioned (cached in memory after first run).
5. Creates a GeoTIFF coverage store pointing at the result's `.tif` file (mounted into GeoServer's data dir).
6. Publishes the coverage as a layer named `model_<modelID>`.
7. Applies the default classified style and marks the result `configured`.

If the store already exists, it is deleted and recreated. Failures are recorded on the result row (`geoserver_status` / `error_message`).

### Risk styles

Eight SLD styles are generated from [`riskStyleDefinitions`](internal/services/geoserver_service.go:42):

| Style                                     | Visible levels    |
| ----------------------------------------- | ----------------- |
| `fire_risk_classified`                    | 1–5 (all)         |
| `fire_risk_moderate_plus`                 | 3–5               |
| `fire_risk_high_plus`                     | 4–5               |
| `fire_risk_level_1` … `fire_risk_level_5` | single level each |

Each style is a raster `ColorMap` with a fixed colour per risk level; hidden levels are rendered with zero opacity.

### Sampling

The service samples the published raster by issuing WMS `GetFeatureInfo` requests over a grid across the layer's bounding box. Two endpoints are exposed:

- **Distribution** — buckets sampled pixels into `very_low` / `low` / `moderate` / `high` / `very_high` counts, plus valid/total sample counts (the ratio approximates the analysed surface area).
- **Grid** — returns positioned samples (`x`, `y`, `value`, `level`, `row`, `column`) for frontend heatmap/choropleth rendering.

## API Endpoints

| Endpoint                                                  | Method | Purpose                                  |
| --------------------------------------------------------- | ------ | ---------------------------------------- |
| `/health`                                                 | GET    | Health check                             |
| `/api/geoserver-proxy/:workspace/wms`                     | ANY    | Proxy WMS requests to GeoServer          |
| `/api/internal/geoserver/results/:id/configure`           | POST   | Provision a GeoServer layer for a result |
| `/api/internal/geoserver/results/:id/layer`               | DELETE | Remove the layer and store for a result  |
| `/api/internal/geoserver/results/:id/bounds`              | GET    | Get the cached layer bounding box        |
| `/api/internal/geoserver/results/:id/sample-distribution` | POST   | Sample raster → risk distribution        |
| `/api/internal/geoserver/results/:id/sample-grid`         | POST   | Sample raster → positioned grid samples  |

The `/api/internal/*` endpoints are intended for the backend API; the WMS proxy is the public path used by the frontend.

## Local development

```bash
make pull       # Pull required images (postgis, geoserver)
make up         # Start infrastructure (postgres, geoserver)
make dev        # Run geoservice locally (go run)
make build      # Build binary
make run        # Build + run
```

### Ports

| Service    | Port |
| ---------- | ---- |
| PostgreSQL | 5433 |
| GeoServer  | 8180 |
| GeoService | 8083 |

### Environment

Copy [`.env.example`](.env.example) to `.env` and adjust as needed. Key variables:

| Variable                    | Default                           | Description                                         |
| --------------------------- | --------------------------------- | --------------------------------------------------- |
| `GEOSERVER_APP_HOST`        | `0.0.0.0`                         | Service bind host                                   |
| `GEOSERVER_APP_PORT`        | `8083`                            | Service port                                        |
| `DB_HOST` / `DB_PORT`       | `localhost` / `5433`              | PostgreSQL connection                               |
| `DB_DATABASE`               | `spatialai`                       | Database name                                       |
| `GEOSERVER_BASE_URL`        | `http://localhost:8180/geoserver` | GeoServer REST/WMS base URL                         |
| `GEOSERVER_ADMIN_USER`      | `admin`                           | GeoServer admin user                                |
| `GEOSERVER_ADMIN_PASSWORD`  | `geoserver`                       | GeoServer admin password                            |
| `GEOSERVER_CONTAINER_MOUNT` | `data/results`                    | Path to result GeoTIFFs inside GeoServer's data dir |
| `APP_TIMEZONE`              | `Europe/Berlin`                   | Application timezone                                |

### Container

```bash
make build-image      # Build Docker image (geoservice:local)
make run-container    # Run the service in a container
make stop-container   # Stop the container
```

The [`Dockerfile`](Dockerfile) builds a static Go binary and runs it standalone. For a combined deployment that runs both GeoServer and the control plane in one container, see [`entrypoint.sh`](entrypoint.sh).

## Setup script

[`geoserver.sh`](geoserver.sh) is a one-shot bootstrap that starts the GeoServer container, waits for it to become ready, creates the `fire_risk` workspace, and uploads the base `fire_risk_classified` SLD style. It is useful for initial environment setup; the Go service provisions the remaining styles and layers at runtime.
