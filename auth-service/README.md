# Auth Service

`platform-core/auth-service` is a Go authentication microservice (Gin + Keycloak + Redis) that handles user login, registration, session management, and email verification for the EnerPlanET platform. It wraps Keycloak's OpenID Connect flows behind a custom API and manages per-brand Keycloak themes.

## Architecture

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  Frontend    │────▶│ Auth Service │────▶│   Keycloak   │
│  (React)     │     │  :8001       │     │  :8080       │
└──────────────┘     └──────┬───────┘     └──────┬───────┘
                            │                     │
                            ▼                     ▼
                     ┌──────────┐          ┌──────────┐
                     │  Redis   │          │PostgreSQL│
                     │ sessions │          │  realm   │
                     └──────────┘          └──────────┘
```

The auth-service is a thin Go layer that:

- Initiates Keycloak OAuth2 flows (login, register, callback)
- Manages session cookies backed by Redis
- Handles password changes, email verification, and password reset
- Provides CSRF protection and rate limiting
- Delegates all user storage to Keycloak (PostgreSQL-backed)

Keycloak itself runs as a separate container with per-brand themes mounted as volumes.

## API Endpoints

| Endpoint                             | Purpose                            |
| ------------------------------------ | ---------------------------------- |
| `GET /api/health`                    | Health check                       |
| `GET /api/csrf-token`                | Get CSRF token                     |
| `POST /api/login`                    | Login (rate-limited)               |
| `POST /api/register`                 | Register (rate-limited)            |
| `GET /api/callback-auth`             | OAuth2 callback                    |
| `POST /api/logout`                   | Logout                             |
| `POST /api/auth/resend-verification` | Resend verification email          |
| `POST /api/auth/forgot-password`     | Forgot password                    |
| `POST /api/auth/refresh-token`       | Refresh session (authenticated)    |
| `POST /api/auth/change-password`     | Change password (authenticated)    |
| `GET /api/auth/tour-status`          | Get tour status (authenticated)    |
| `POST /api/auth/complete-tour`       | Mark tour complete (authenticated) |
| `GET /api/auth/keep-alive`           | Session keep-alive (authenticated) |

## Theme System

Keycloak themes provide branded login pages, email templates, and error pages. The platform supports multiple brands via separate theme directories mounted into Keycloak at runtime.

### Current themes

| Theme directory               | Brand name | Mounted as   | Used by realm |
| ----------------------------- | ---------- | ------------ | ------------- |
| `keycloak/themes/enerplanet/` | EnerPlanET | `enerplanet` | spatialhub    |
| `keycloak/themes/storcito/`   | Storcito   | `storcito`   | —             |

The realm configuration in [`keycloak/imports/keycloak-realm.json`](keycloak/imports/keycloak-realm.json) sets `loginTheme`, `emailTheme`, and `accountTheme` to `"spatialhub"`. The [`docker-compose.yml`](../docker-compose.yml:55) maps `themes/enerplanet` to both `enerplanet` and `spatialhub` theme names, so the EnerPlanET theme serves both.

### Theme structure

Each theme follows the [Keycloak theme layout](https://www.keycloak.org/docs/latest/server_development/#_themes):

```
themes/<name>/
├── theme.properties          # Parent, import, name
├── email/
│   ├── theme.properties
│   ├── html/                 # HTML email templates (.ftl)
│   ├── text/                 # Plain-text email templates (.ftl)
│   └── messages/
│       └── messages_en.properties  # Email subject lines
└── login/
    ├── theme.properties
    ├── template.ftl          # Login page layout
    ├── error.ftl
    ├── info.ftl
    ├── login-actions.ftl
    ├── login-update-password.ftl
    └── resources/
        ├── css/styles.css
        └── img/logo.png
```

## Adding a new theme

### 1. Create the theme directory

Copy an existing theme as a starting point:

```bash
cp -r keycloak/themes/enerplanet keycloak/themes/<your-brand>
```

### 2. Update `theme.properties`

Edit [`keycloak/themes/<your-brand>/theme.properties`](keycloak/themes/enerplanet/theme.properties):

```properties
parent=base
import=common/keycloak
name=<your-brand>
```

### 3. Brand the login page

- **Logo**: Replace `login/resources/img/logo.png` with your brand's logo.
- **Title**: In [`login/template.ftl`](keycloak/themes/enerplanet/login/template.ftl), change the fallback display name on line 7:
  ```ftl
  <title>${msg("loginTitle",(realm.displayName!'<Your Brand>'))}</title>
  ```
- **Footer**: Update the footer brand name and copyright.
- **CSS**: Adjust colours in [`login/resources/css/styles.css`](keycloak/themes/enerplanet/login/resources/css/styles.css).

### 4. Brand the email templates

Update brand references in all `.ftl` files under `email/html/` and `email/text/`:

- `email/html/email-verification.ftl` — title, header, body text, footer
- `email/html/executeActions.ftl` — title, header, body text, footer
- `email/text/email-verification.ftl` — welcome text, footer
- `email/text/executeActions.ftl` — body text, footer

### 5. Update email subject lines

Edit [`email/messages/messages_en.properties`](keycloak/themes/enerplanet/email/messages/messages_en.properties):

```properties
emailVerificationSubject=Verify Your <Brand> Account
passwordResetSubject=Password Reset Request - <Brand>
executeActionsSubject=Account Update Required - <Brand>
```

### 6. Mount the theme in docker-compose

Add a volume mount in [`platform-core/docker-compose.yml`](../docker-compose.yml:52):

```yaml
volumes:
  - ./auth-service/keycloak/themes/<your-brand>:/opt/keycloak/themes/<your-brand>:ro
```

### 7. Set the theme in the realm

Update [`keycloak/imports/keycloak-realm.json`](keycloak/imports/keycloak-realm.json) to reference your theme:

```json
"loginTheme": "<your-brand>",
"emailTheme": "<your-brand>",
"accountTheme": "<your-brand>"
```

Or, if you want the theme to be selectable per-realm at runtime, configure it via the Keycloak admin console instead.

### 8. Restart Keycloak

```bash
docker compose up -d keycloak
```

The init container will re-import the realm configuration on next startup.

## Local development

```bash
make pull       # Pull required images (postgres, redis, keycloak)
make up         # Start infrastructure services
make dev        # Run auth-service locally (go run)
make build      # Build binary
make run        # Build + run
```

### Ports

| Service      | Port |
| ------------ | ---- |
| PostgreSQL   | 5433 |
| Redis        | 6379 |
| Keycloak     | 8080 |
| Auth Service | 8001 |

### Environment

Copy [`.env.example`](.env.example) to `.env` and adjust as needed. Key variables:

| Variable                 | Default                          | Description              |
| ------------------------ | -------------------------------- | ------------------------ |
| `KEYCLOAK_URL`           | `http://localhost:8080/keycloak` | Keycloak server URL      |
| `KEYCLOAK_REALM`         | `spatialhub`                     | Keycloak realm           |
| `KEYCLOAK_CLIENT_ID`     | `spatialhub`                     | OAuth2 client ID         |
| `KEYCLOAK_CLIENT_SECRET` | —                                | OAuth2 client secret     |
| `REDIS_PASSWORD`         | `redis_password`                 | Redis password           |
| `FRONTEND_URL`           | `http://localhost:3000`          | Frontend origin for CORS |
| `SESSION_TTL_MINUTES`    | `60`                             | Session lifetime         |
