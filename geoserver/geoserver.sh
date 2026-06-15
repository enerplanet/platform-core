#!/bin/bash

# GeoServer setup script

set -e

# Configuration
CONTAINER_NAME="geoserver"
IMAGE="kartoza/geoserver:2.25.2"
GEOSERVER_PORT="8180"
GEOSERVER_INTERNAL_PORT="8080"
GEOSERVER_URL="http://localhost:${GEOSERVER_PORT}/geoserver"
USERNAME="admin"
PASSWORD="geoserver"
WORKSPACE="fire_risk"
STYLE_NAME="fire_risk_classified"
NETWORK="spatialhub-net"

# Logging
log() { echo "[$(date '+%H:%M:%S')] $1"; }
error() { echo "[ERROR] $1"; }
success() { echo "[OK] $1"; }

# Start GeoServer container
start_container() {
    log "Starting GeoServer container..."
    
    # Resolve absolute path for backend storage (ensure it exists)
    SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
    BACKEND_STORAGE_ABS="$(cd "$SCRIPT_DIR/../backend/storage/data" 2>/dev/null && pwd)"
    
    # Create storage directory if it doesn't exist
    if [ -z "$BACKEND_STORAGE_ABS" ]; then
        # Fallback if directory doesn't exist yet
        mkdir -p "$SCRIPT_DIR/../backend/storage/data"
        BACKEND_STORAGE_ABS="$(cd "$SCRIPT_DIR/../backend/storage/data" && pwd)"
    fi
    
    # Fix permissions (ignore errors if not owner)
    chmod 777 "$BACKEND_STORAGE_ABS" 2>/dev/null || true
    
    log "Ensuring data directory exists and is writable: $BACKEND_STORAGE_ABS"

    # Run using docker compose
    docker compose up -d
    
    if [ $? -eq 0 ]; then
        success "GeoServer container started"
        return 0
    else
        error "Failed to start GeoServer container"
        return 1
    fi
}

# Wait for GeoServer to be ready
wait_for_geoserver() {
    log "Waiting 30 seconds for GeoServer to initialize..."
    sleep 30
    
    log "Checking GeoServer availability..."
    # Loop for up to 60 more seconds
    for i in {1..12}; do
        if curl -sf "$GEOSERVER_URL/web" > /dev/null 2>&1; then
            success "GeoServer is ready"
            return 0
        fi
        log "Waiting for GeoServer... ($((i*5))s)"
        sleep 5
    done
    
    log "GeoServer might still be starting, but proceeding with configuration attempts..."
    return 0
}

# Create workspace
create_workspace() {
    log "Creating workspace: $WORKSPACE"
    
    # Check if workspace exists
    if curl -sf "$GEOSERVER_URL/rest/workspaces/$WORKSPACE" -u "$USERNAME:$PASSWORD" > /dev/null 2>&1; then
        log "Workspace '$WORKSPACE' already exists"
        return 0
    fi
    
    # Create workspace
    curl -X POST "$GEOSERVER_URL/rest/workspaces" \
        -u "$USERNAME:$PASSWORD" \
        -H "Content-Type: application/json" \
        -d "{\"workspace\":{\"name\":\"$WORKSPACE\"}}" \
        -s > /dev/null
    
    if [ $? -eq 0 ]; then
        success "Workspace '$WORKSPACE' created"
    fi
}

# Create fire risk style
create_style() {
    log "Creating fire risk classification style..."
    
    # Check if style exists and delete it to update
    if curl -sf "$GEOSERVER_URL/rest/styles/$STYLE_NAME" -u "$USERNAME:$PASSWORD" > /dev/null 2>&1; then
        log "Style '$STYLE_NAME' already exists - updating"
        curl -X DELETE "$GEOSERVER_URL/rest/styles/$STYLE_NAME?purge=true" \
            -u "$USERNAME:$PASSWORD" -s > /dev/null 2>&1
    fi
    
    # Create SLD file
    SLD_CONTENT='<?xml version="1.0" encoding="UTF-8"?>
<sld:StyledLayerDescriptor xmlns:sld="http://www.opengis.net/sld"
                           xmlns:ogc="http://www.opengis.net/ogc"
                           version="1.0.0">
  <sld:NamedLayer>
    <sld:Name>fire_risk_transparent</sld:Name>
    <sld:UserStyle>
      <sld:Title>Fire Risk with Transparent Background</sld:Title>
      <sld:FeatureTypeStyle>
        <sld:Rule>
          <sld:RasterSymbolizer>
            <sld:Opacity>1.0</sld:Opacity>
            <sld:ColorMap type="values">
              <sld:ColorMapEntry color="#000000" quantity="-9999" opacity="0.0" label="No Data"/>
              <sld:ColorMapEntry color="#2563eb" quantity="1" opacity="1.0" label="Very Low Risk"/>
              <sld:ColorMapEntry color="#16a34a" quantity="2" opacity="1.0" label="Low Risk"/>
              <sld:ColorMapEntry color="#eab308" quantity="3" opacity="1.0" label="Medium Risk"/>
              <sld:ColorMapEntry color="#ea580c" quantity="4" opacity="1.0" label="High Risk"/>
              <sld:ColorMapEntry color="#dc2626" quantity="5" opacity="1.0" label="Very High Risk"/>
            </sld:ColorMap>
            <sld:ContrastEnhancement/>
          </sld:RasterSymbolizer>
        </sld:Rule>
      </sld:FeatureTypeStyle>
    </sld:UserStyle>
  </sld:NamedLayer>
</sld:StyledLayerDescriptor>'
    
    # Create style
    curl -X POST "$GEOSERVER_URL/rest/styles" \
        -u "$USERNAME:$PASSWORD" \
        -H "Content-Type: application/json" \
        -d "{\"style\":{\"name\":\"$STYLE_NAME\",\"filename\":\"$STYLE_NAME.sld\"}}" \
        -s > /dev/null
    
    # Upload SLD content
    echo "$SLD_CONTENT" | curl -X PUT "$GEOSERVER_URL/rest/styles/$STYLE_NAME" \
        -u "$USERNAME:$PASSWORD" \
        -H "Content-Type: application/vnd.ogc.sld+xml" \
        -d @- \
        -s > /dev/null
    
    success "Style '$STYLE_NAME' created"
}

# Main
main() {
    log "Starting GeoServer setup..."
    
    start_container
    wait_for_geoserver
    create_workspace
    create_style
    
    success "GeoServer setup completed"
}

main "$@"
