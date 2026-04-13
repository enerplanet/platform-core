#!/bin/sh
# Combined entrypoint that runs both GeoServer (Tomcat) and the Go control-plane service

set -eu

APP_BIN="/usr/local/bin/geoserver-service"
GEOSERVER_CMD="/scripts/start.sh"

if [ ! -x "$APP_BIN" ]; then
  echo "[entrypoint] missing GeoServer control-plane binary at $APP_BIN" >&2
  exit 1
fi

if [ ! -x "$GEOSERVER_CMD" ]; then
  echo "[entrypoint] expected GeoServer start script at $GEOSERVER_CMD" >&2
  exit 1
fi

"$APP_BIN" &
APP_PID=$!

"$GEOSERVER_CMD" "$@" &
GEOSERVER_PID=$!

terminate_children() {
  kill -TERM "$APP_PID" 2>/dev/null || true
  kill -TERM "$GEOSERVER_PID" 2>/dev/null || true
}

trap 'terminate_children; wait "$APP_PID" 2>/dev/null || true; wait "$GEOSERVER_PID" 2>/dev/null || true; exit 0' INT TERM

while true; do
  if ! kill -0 "$GEOSERVER_PID" 2>/dev/null; then
    wait "$GEOSERVER_PID"
    STATUS=$?
    kill -TERM "$APP_PID" 2>/dev/null || true
    wait "$APP_PID" 2>/dev/null || true
    exit $STATUS
  fi

  if ! kill -0 "$APP_PID" 2>/dev/null; then
    wait "$APP_PID"
    STATUS=$?
    kill -TERM "$GEOSERVER_PID" 2>/dev/null || true
    wait "$GEOSERVER_PID" 2>/dev/null || true
    exit $STATUS
  fi

  sleep 2
done
