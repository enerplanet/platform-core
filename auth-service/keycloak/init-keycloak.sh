#!/bin/bash
set -e

echo "==> Keycloak Realm Import Script"
echo "Started at: $(date)"

# Save command-line passed variables before loading .env
INIT_KEYCLOAK_URL="${KEYCLOAK_URL:-}"
INIT_ADMIN_USER="${KEYCLOAK_ADMIN_USER:-}"
INIT_ADMIN_PASS="${KEYCLOAK_ADMIN_PASSWORD:-}"
INIT_SERVER_URL="${SERVER_URL:-}"

# Load environment variables from .env file
if [ -f /backend-config/.env ]; then
  # Safe .env loader: no eval, supports special chars like ! and &
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    case "$line" in \#*) continue;; esac
    case "$line" in
      *=*)
        key="${line%%=*}"
        val="${line#*=}"
        # strip optional surrounding quotes
        if [[ "$val" =~ ^\".*\"$ || "$val" =~ ^\'.*\'$ ]]; then
          val="${val:1:${#val}-2}"
        fi
        export "$key=$val"
      ;;
    esac
  done < /backend-config/.env
fi

# Command-line passed variables take priority over .env file
[ -n "$INIT_KEYCLOAK_URL" ] && KEYCLOAK_URL="$INIT_KEYCLOAK_URL"
[ -n "$INIT_ADMIN_USER" ] && KEYCLOAK_ADMIN_USER="$INIT_ADMIN_USER"
[ -n "$INIT_ADMIN_PASS" ] && KEYCLOAK_ADMIN_PASSWORD="$INIT_ADMIN_PASS"
[ -n "$INIT_SERVER_URL" ] && SERVER_URL="$INIT_SERVER_URL"

KEYCLOAK_URL="${KEYCLOAK_URL:-http://keycloak:8080}"
ADMIN_USER="${KEYCLOAK_ADMIN_USER:-admin}"
ADMIN_PASS="${KEYCLOAK_ADMIN_PASSWORD:-admin}"
# Per-brand realm support. Set REALM_NAME / KEYCLOAK_CLIENT_ID / REALM_FILE in the
# environment to provision a specific brand realm (e.g. enerplanet, storcito).
# Defaults preserve the legacy single-realm ("spatialhub") behaviour.
REALM_NAME="${REALM_NAME:-spatialhub}"
CLIENT_ID="${KEYCLOAK_CLIENT_ID:-$REALM_NAME}"
REALM_FILE="${REALM_FILE:-/opt/keycloak/data/import/imports/keycloak-realm.json}"
if [ ! -f "$REALM_FILE" ] && [ -f "/opt/keycloak/data/import/keycloak-realm.json" ]; then
  REALM_FILE="/opt/keycloak/data/import/keycloak-realm.json"
fi

echo "Configuration:"
echo "  Keycloak URL: $KEYCLOAK_URL"
echo "  Admin User: $ADMIN_USER"
echo "  Realm: $REALM_NAME"
echo "  Client ID: $CLIENT_ID"
echo ""

# Function to check if Keycloak is reachable
check_keycloak_connection() {
  curl -sf "$1" >/dev/null 2>&1
  return $?
}

ensure_service_account_roles() {
  local client_uuid="$1"

  if [ -z "$client_uuid" ] || [ "$client_uuid" = "null" ]; then
    echo "WARNING: Cannot ensure service account roles without a valid client UUID"
    return
  fi

  echo "Ensuring realm-management roles for service account of client '$CLIENT_ID'..."

  local service_account_data service_account_user_id
  service_account_data=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME/clients/$client_uuid/service-account-user" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json")
  service_account_user_id=$(echo "$service_account_data" | jq -r '.id')

  if [ -z "$service_account_user_id" ] || [ "$service_account_user_id" = "null" ]; then
    echo "WARNING: Could not resolve service-account user for client '$CLIENT_ID'"
    echo "Response: $service_account_data"
    return
  fi

  local realm_mgmt_data realm_mgmt_client_uuid
  realm_mgmt_data=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME/clients?clientId=realm-management" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json")
  realm_mgmt_client_uuid=$(echo "$realm_mgmt_data" | jq -r '.[0].id')

  if [ -z "$realm_mgmt_client_uuid" ] || [ "$realm_mgmt_client_uuid" = "null" ]; then
    echo "WARNING: Could not resolve realm-management client"
    echo "Response: $realm_mgmt_data"
    return
  fi

  local available_roles role_payload role_count assign_response assign_http_code assign_body
  available_roles=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME/users/$service_account_user_id/role-mappings/clients/$realm_mgmt_client_uuid/available" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json")

  role_payload=$(echo "$available_roles" | jq '[
    .[]
    | select(
        .name == "manage-users"
        or .name == "view-users"
        or .name == "query-users"
        or .name == "query-groups"
      )
    | {id, name}
  ]')
  role_count=$(echo "$role_payload" | jq 'length')

  if [ "$role_count" = "0" ]; then
    echo "✓ Service-account roles already present"
    return
  fi

  assign_response=$(curl -s -X POST "$KEYCLOAK_URL/admin/realms/$REALM_NAME/users/$service_account_user_id/role-mappings/clients/$realm_mgmt_client_uuid" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    -d "$role_payload" \
    -w "\n%{http_code}")

  assign_http_code=$(echo "$assign_response" | tail -n 1)
  assign_body=$(echo "$assign_response" | sed '$d')

  if [ "$assign_http_code" = "204" ] || [ "$assign_http_code" = "200" ]; then
    echo "✓ Service-account roles synchronized"
  else
    echo "WARNING: Failed to assign service-account roles (HTTP $assign_http_code)"
    [ -n "$assign_body" ] && echo "Response: $assign_body"
  fi
}

update_user_profile_configuration() {
  local realm_file="$REALM_FILE"

  if [ ! -f "$realm_file" ]; then
    echo "WARNING: Could not find realm file for user profile configuration"
    return
  fi

  echo "Using realm file: $realm_file"

  local desired_config
  desired_config=$(jq -r '.attributes.userProfileConfiguration // empty' "$realm_file")

  if [ -z "$desired_config" ]; then
    return
  fi

  echo "Synchronizing Keycloak user profile configuration..."

  # Step 1: Enable user profile at realm level
  echo "Enabling declarative user profile for realm..."
  curl -s -X PUT "$KEYCLOAK_URL/admin/realms/$REALM_NAME" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"attributes":{"userProfileEnabled":"true"}}' >/dev/null 2>&1

  # Step 2: Check if declarative-user-profile component exists
  local existing_component
  existing_component=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME/components?type=org.keycloak.userprofile.UserProfileProvider" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" | jq -r '.[] | select(.providerId == "declarative-user-profile") | .id')

  if [ -n "$existing_component" ] && [ "$existing_component" != "null" ]; then
    echo "Updating existing user profile component: $existing_component"

    # Fetch the existing component to get all its fields
    local existing_data
    existing_data=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME/components/$existing_component" \
      -H "Authorization: Bearer $ACCESS_TOKEN" \
      -H "Content-Type: application/json")

    # Update only the config, keeping all other fields
    local update_payload
    update_payload=$(echo "$existing_data" | jq \
      --arg config "$desired_config" '
      .config["kc.user.profile.config"] = [$config]
      ')

    local response http_code body
    response=$(curl -s -X PUT "$KEYCLOAK_URL/admin/realms/$REALM_NAME/components/$existing_component" \
      -H "Authorization: Bearer $ACCESS_TOKEN" \
      -H "Content-Type: application/json" \
      -d "$update_payload" \
      -w "\n%{http_code}")

    http_code=$(echo "$response" | tail -n 1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "204" ] || [ "$http_code" = "200" ]; then
      echo "✓ User profile component updated"
    else
      echo "WARNING: Failed to update component (HTTP $http_code)"
      [ -n "$body" ] && echo "Response: $body"
    fi
  else
    echo "Creating declarative user profile component..."

    local create_payload
    create_payload=$(jq -n \
      --arg config "$desired_config" '{
        name: "user-profile",
        providerId: "declarative-user-profile",
        providerType: "org.keycloak.userprofile.UserProfileProvider",
        config: {
          "kc.user.profile.config": [$config]
        }
      }')

    local response http_code body
    response=$(curl -s -X POST "$KEYCLOAK_URL/admin/realms/$REALM_NAME/components" \
      -H "Authorization: Bearer $ACCESS_TOKEN" \
      -H "Content-Type: application/json" \
      -d "$create_payload" \
      -w "\n%{http_code}")

    http_code=$(echo "$response" | tail -n 1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_code" = "201" ] || [ "$http_code" = "200" ]; then
      echo "✓ User profile component created"
    else
      echo "WARNING: Failed to create component (HTTP $http_code)"
      [ -n "$body" ] && echo "Response: $body"
    fi
  fi
}

# Wait for Keycloak token endpoint to be ready (port may be open before realms are loaded)
echo "Waiting for Keycloak to be fully ready..."
MAX_WAIT=60
WAITED=0
while [ $WAITED -lt $MAX_WAIT ]; do
  if curl -sf "$KEYCLOAK_URL/realms/master" >/dev/null 2>&1; then
    echo "✓ Keycloak master realm is ready"
    break
  fi
  echo "  Waiting for master realm... (${WAITED}s/${MAX_WAIT}s)"
  sleep 5
  WAITED=$((WAITED + 5))
done
if [ $WAITED -ge $MAX_WAIT ]; then
  echo "WARNING: Timed out waiting for master realm, attempting token request anyway..."
fi

# Get admin token
echo "Getting admin access token..."
TOKEN_RESPONSE=$(curl -sf -X POST "$KEYCLOAK_URL/realms/master/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "username=$ADMIN_USER" \
  -d "password=$ADMIN_PASS" \
  -d 'grant_type=password' \
  -d 'client_id=admin-cli' 2>/dev/null || echo "")

if [ -n "$TOKEN_RESPONSE" ]; then
  ACCESS_TOKEN=$(echo "$TOKEN_RESPONSE" | jq -r '.access_token' 2>/dev/null)
  if [ -n "$ACCESS_TOKEN" ] && [ "$ACCESS_TOKEN" != "null" ]; then
    echo "✓ Successfully obtained admin access token"
  fi
fi

if [ -z "$ACCESS_TOKEN" ] || [ "$ACCESS_TOKEN" = "null" ]; then
  echo ""
  echo "================================================"
  echo "ERROR: Could not authenticate with Keycloak"
  echo "================================================"
  echo "Debug information:"
  echo "  URL: $KEYCLOAK_URL/realms/master/protocol/openid-connect/token"
  echo "  Admin user: $ADMIN_USER"
  echo "  Last response: ${TOKEN_RESPONSE:0:200}"
  echo ""
  echo "Attempting verbose connection test..."
  curl -v "$KEYCLOAK_URL/realms/master/protocol/openid-connect/token" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    -d "username=$ADMIN_USER" \
    -d "password=$ADMIN_PASS" \
    -d 'grant_type=password' \
    -d 'client_id=admin-cli' 2>&1 | head -50
  exit 1
fi

echo "==> Checking if realm '$REALM_NAME' exists..."

if [ -z "$TOKEN_RESPONSE" ]; then
  echo "ERROR: Failed to get admin access token"
  exit 1
fi

ACCESS_TOKEN=$(echo "$TOKEN_RESPONSE" | jq -r '.access_token')

if [ -z "$ACCESS_TOKEN" ] || [ "$ACCESS_TOKEN" = "null" ]; then
  echo "ERROR: Could not extract access token"
  exit 1
fi

# Check if realm exists
REALM_CHECK=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -w "\n%{http_code}" 2>/dev/null)

HTTP_CODE=$(echo "$REALM_CHECK" | tail -n 1)
REALM_DATA=$(echo "$REALM_CHECK" | sed '$d')

if [ "$HTTP_CODE" = "200" ]; then
  echo "✓ Realm '$REALM_NAME' already exists - updating realm settings, client URLs and retrieving secret"

  # Update realm settings from JSON if file exists
  REALM_JSON_FILE="$REALM_FILE"

  if [ -f "$REALM_JSON_FILE" ]; then
    echo "Updating realm settings from $REALM_JSON_FILE..."
    REALM_UPDATE=$(jq '{
      accessTokenLifespan,
      accessTokenLifespanForImplicitFlow,
      ssoSessionIdleTimeout,
      ssoSessionMaxLifespan,
      clientSessionIdleTimeout,
      clientSessionMaxLifespan,
      offlineSessionIdleTimeout,
      accessCodeLifespan,
      accessCodeLifespanUserAction,
      accessCodeLifespanLogin,
      actionTokenGeneratedByAdminLifespan,
      actionTokenGeneratedByUserLifespan
    } | with_entries(select(.value != null))' "$REALM_JSON_FILE")

    curl -s -X PUT "$KEYCLOAK_URL/admin/realms/$REALM_NAME" \
      -H "Authorization: Bearer $ACCESS_TOKEN" \
      -H "Content-Type: application/json" \
      -d "$REALM_UPDATE" > /dev/null
    echo "✓ Realm settings updated"
  fi

  update_user_profile_configuration

  SERVER_URL_EFFECTIVE="$SERVER_URL"
  echo "Original SERVER_URL: $SERVER_URL"

  # Only convert to HTTPS for production domains (not localhost)
  if [[ ! "$SERVER_URL_EFFECTIVE" =~ ^http://localhost ]]; then
    if [[ "$SERVER_URL_EFFECTIVE" =~ ^http:// ]]; then
      SERVER_URL_EFFECTIVE="${SERVER_URL_EFFECTIVE/http:/https:}"
      echo "Converted to HTTPS: $SERVER_URL_EFFECTIVE"
    fi
  fi

  echo "Using SERVER_URL_EFFECTIVE: $SERVER_URL_EFFECTIVE"

  # Find client
  echo "Fetching client data for '$CLIENT_ID'..."
  CLIENT_DATA=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME/clients?clientId=$CLIENT_ID" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json")
  CLIENT_UUID=$(echo "$CLIENT_DATA" | jq -r '.[0].id')

  if [ -z "$CLIENT_UUID" ] || [ "$CLIENT_UUID" = "null" ]; then
    echo "ERROR: Could not find client '$CLIENT_ID' to update"
    echo "Client data response: $CLIENT_DATA"
    exit 1
  fi

  echo "✓ Found client UUID: $CLIENT_UUID"

  # Fetch current client config to preserve other settings
  echo "Fetching current client configuration..."
  CURRENT_CLIENT=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME/clients/$CLIENT_UUID" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json")

  # Build updated client payload with all fields
  echo "Building updated client configuration..."
  UPDATED_CLIENT_PAYLOAD=$(echo "$CURRENT_CLIENT" | jq \
    --arg ru "$SERVER_URL_EFFECTIVE" \
    --arg bu "$SERVER_URL_EFFECTIVE" \
    --arg au "$SERVER_URL_EFFECTIVE" \
    --argjson redirects "[\"$SERVER_URL_EFFECTIVE/*\", \"$SERVER_URL_EFFECTIVE/callback-auth\"]" \
    --argjson origins "[\"$SERVER_URL_EFFECTIVE\"]" \
    '.rootUrl=$ru
    | .baseUrl=$bu
    | .adminUrl=$au
    | .redirectUris=$redirects
    | .webOrigins=$origins
    | .serviceAccountsEnabled=true
    | .publicClient=false
    | .clientAuthenticatorType="client-secret"
    | .attributes["post.logout.redirect.uris"]=$ru + "/*"')

  echo "Payload preview:"
  echo "$UPDATED_CLIENT_PAYLOAD" | jq '{rootUrl, baseUrl, adminUrl, redirectUris, webOrigins, attributes: {("post.logout.redirect.uris"): .attributes["post.logout.redirect.uris"]}}'

  # Update client
  echo "Updating client configuration..."
  UPDATE_RESPONSE=$(curl -s -X PUT "$KEYCLOAK_URL/admin/realms/$REALM_NAME/clients/$CLIENT_UUID" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    -d "$UPDATED_CLIENT_PAYLOAD" \
    -w "\n%{http_code}")

  UPDATE_HTTP_CODE=$(echo "$UPDATE_RESPONSE" | tail -n 1)
  UPDATE_BODY=$(echo "$UPDATE_RESPONSE" | sed '$d')

  if [ "$UPDATE_HTTP_CODE" = "204" ] || [ "$UPDATE_HTTP_CODE" = "200" ]; then
    echo "✓ Client URLs updated successfully (HTTP $UPDATE_HTTP_CODE)"
  else
    echo "WARNING: Client update returned HTTP $UPDATE_HTTP_CODE"
    echo "Response: $UPDATE_BODY"
  fi

  ensure_service_account_roles "$CLIENT_UUID"

  # Retrieve client secret
  echo ""
  echo "==> Retrieving client secret..."

  # Try to get existing secret first
  SECRET_DATA=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME/clients/$CLIENT_UUID/client-secret" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json")

  CLIENT_SECRET=$(echo "$SECRET_DATA" | jq -r '.value')

  if [ -z "$CLIENT_SECRET" ] || [ "$CLIENT_SECRET" = "null" ]; then
      echo "No existing secret found, regenerating..."
      REGEN_RESPONSE=$(curl -s -X POST "$KEYCLOAK_URL/admin/realms/$REALM_NAME/clients/$CLIENT_UUID/client-secret" \
        -H "Authorization: Bearer $ACCESS_TOKEN" \
        -H "Content-Type: application/json" \
        -w "\n%{http_code}")

      REGEN_HTTP_CODE=$(echo "$REGEN_RESPONSE" | tail -n 1)
      REGEN_BODY=$(echo "$REGEN_RESPONSE" | sed '$d')
      CLIENT_SECRET=$(echo "$REGEN_BODY" | jq -r '.value')
  else
      echo "✓ Found existing client secret"
  fi

  if [ -z "$CLIENT_SECRET" ] || [ "$CLIENT_SECRET" = "null" ] || [ "$CLIENT_SECRET" = "**********" ]; then
    echo "ERROR: Could not retrieve valid client secret"
    echo "Response: $REGEN_BODY"
    exit 1
  else
    echo "✓ Client secret obtained successfully"

    # Function to update .env file with client secret
    update_env_file() {
      local config_dir="$1"
      local service_name="$2"

      if [ -d "$config_dir" ]; then
        # Update .env if exists
        if [ -f "$config_dir/.env" ]; then
          if grep -q "^KEYCLOAK_CLIENT_SECRET=" "$config_dir/.env"; then
            grep -v "^KEYCLOAK_CLIENT_SECRET=" "$config_dir/.env" > /tmp/.env.tmp
            echo "KEYCLOAK_CLIENT_SECRET=$CLIENT_SECRET" >> /tmp/.env.tmp
            cat /tmp/.env.tmp > "$config_dir/.env"
            rm /tmp/.env.tmp
          else
            echo "KEYCLOAK_CLIENT_SECRET=$CLIENT_SECRET" >> "$config_dir/.env"
          fi
          echo "✓ Updated $service_name .env"
        fi
      fi
    }

    echo ""
    echo "==> Updating all service .env files with client secret..."

    # Update backend
    update_env_file "/backend-config" "backend"

    # Update auth-service
    update_env_file "/auth-service-config" "auth-service"

    # Update webservice
    update_env_file "/webservice-config" "webservice"


    echo ""
    echo "=========================================="
    echo "✓ Configuration Complete!"
    echo "=========================================="
    echo ""
    echo "The client secret has been saved to all service .env files."
    echo ""
  fi

else
  echo "==> Realm '$REALM_NAME' does not exist (HTTP $HTTP_CODE) - importing..."

  if [ ! -f "$REALM_FILE" ]; then
    echo "ERROR: Realm file not found at $REALM_FILE"
    echo "Listing files in /opt/keycloak/data/import/imports/:"
    ls -la /opt/keycloak/data/import/imports/ || echo "Directory not accessible"
    exit 1
  fi

  SERVER_URL="${SERVER_URL:-http://localhost:8000}"
  echo "Using server URL: $SERVER_URL"

  echo "Updating URLs in realm configuration..."
  cp "$REALM_FILE" /tmp/keycloak-realm-updated.json

  sed -i "s|http://localhost:8000|$SERVER_URL|g" /tmp/keycloak-realm-updated.json
  sed -i "s|http://localhost:3000|$SERVER_URL|g" /tmp/keycloak-realm-updated.json
  sed -i "s|http://localhost:8180|$SERVER_URL|g" /tmp/keycloak-realm-updated.json
  sed -i "s|http://localhost:8080|$SERVER_URL|g" /tmp/keycloak-realm-updated.json

  echo "✓ URLs updated to $SERVER_URL"
  echo "Found realm file, importing..."

  IMPORT_RESPONSE=$(curl -s -X POST "$KEYCLOAK_URL/admin/realms" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    -d @/tmp/keycloak-realm-updated.json \
    -w "\n%{http_code}")

  IMPORT_HTTP_CODE=$(echo "$IMPORT_RESPONSE" | tail -n 1)
  IMPORT_BODY=$(echo "$IMPORT_RESPONSE" | sed '$d')

  if [ "$IMPORT_HTTP_CODE" = "201" ] || [ "$IMPORT_HTTP_CODE" = "200" ]; then
    echo "✓ Realm '$REALM_NAME' imported successfully (HTTP $IMPORT_HTTP_CODE)"
  else
    echo "ERROR: Failed to import realm (HTTP $IMPORT_HTTP_CODE)"
    echo "Response: $IMPORT_BODY"
    exit 1
  fi

  update_user_profile_configuration

  echo ""
  echo "==> Retrieving client secret for backend configuration..."

  CLIENT_DATA=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME/clients?clientId=$CLIENT_ID" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json")

  CLIENT_UUID=$(echo "$CLIENT_DATA" | jq -r '.[0].id')

  if [ -z "$CLIENT_UUID" ] || [ "$CLIENT_UUID" = "null" ]; then
    echo "ERROR: Could not find client '$CLIENT_ID' in realm '$REALM_NAME'"
    echo "Response: $CLIENT_DATA"
    exit 1
  fi

  echo "✓ Found client UUID: $CLIENT_UUID"

  ensure_service_account_roles "$CLIENT_UUID"

  echo ""
  echo "==> Regenerating client secret..."

  # Regenerate the secret (POST to regenerate-secret endpoint)
  REGEN_RESPONSE=$(curl -s -X POST "$KEYCLOAK_URL/admin/realms/$REALM_NAME/clients/$CLIENT_UUID/client-secret" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    -w "\n%{http_code}")

  REGEN_HTTP_CODE=$(echo "$REGEN_RESPONSE" | tail -n 1)
  REGEN_BODY=$(echo "$REGEN_RESPONSE" | sed '$d')

  if [ "$REGEN_HTTP_CODE" = "200" ]; then
    echo "✓ Client secret regenerated"
    CLIENT_SECRET=$(echo "$REGEN_BODY" | jq -r '.value')
  else
    echo "WARNING: Secret regeneration returned HTTP $REGEN_HTTP_CODE, trying to retrieve existing secret..."
    # Fallback: try to get existing secret
    SECRET_DATA=$(curl -s -X GET "$KEYCLOAK_URL/admin/realms/$REALM_NAME/clients/$CLIENT_UUID/client-secret" \
      -H "Authorization: Bearer $ACCESS_TOKEN" \
      -H "Content-Type: application/json")
    CLIENT_SECRET=$(echo "$SECRET_DATA" | jq -r '.value')
  fi

  if [ -z "$CLIENT_SECRET" ] || [ "$CLIENT_SECRET" = "null" ] || [ "$CLIENT_SECRET" = "**********" ]; then
    echo "ERROR: Could not retrieve valid client secret"
    echo "You may need to regenerate the secret manually in Keycloak admin console"
  else
    echo "✓ Client secret obtained successfully"

    # Function to update .env file with client secret
    update_env_file() {
      local config_dir="$1"
      local service_name="$2"

      if [ -d "$config_dir" ]; then
        # Update .env if exists
        if [ -f "$config_dir/.env" ]; then
          if grep -q "^KEYCLOAK_CLIENT_SECRET=" "$config_dir/.env"; then
            grep -v "^KEYCLOAK_CLIENT_SECRET=" "$config_dir/.env" > /tmp/.env.tmp
            echo "KEYCLOAK_CLIENT_SECRET=$CLIENT_SECRET" >> /tmp/.env.tmp
            cat /tmp/.env.tmp > "$config_dir/.env"
            rm /tmp/.env.tmp
          else
            echo "KEYCLOAK_CLIENT_SECRET=$CLIENT_SECRET" >> "$config_dir/.env"
          fi
          echo "✓ Updated $service_name .env"
        fi
      fi
    }

    echo ""
    echo "==> Updating all service .env files with client secret..."

    # Update backend
    update_env_file "/backend-config" "backend"

    # Update auth-service
    update_env_file "/auth-service-config" "auth-service"

    # Update webservice
    update_env_file "/webservice-config" "webservice"


    echo ""
    echo "=========================================="
    echo "✓ Keycloak Configuration Complete!"
    echo "=========================================="
    echo ""
    echo "The client secret has been saved to all service .env files."
    echo ""
    echo "Next Steps:"
    echo "  1. Build the services:      make build-all"
    echo "  2. Start the services:      make up-services"
    echo "  3. Run frontend dev:        make dev"
    echo ""
    echo "Or build and start individually:"
    echo "  make build-auth && make up-auth"
    echo "  make build-webservice && make up-webservice"
    echo ""
  fi
fi
