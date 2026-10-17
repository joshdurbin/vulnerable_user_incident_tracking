#!/bin/sh
# Write the browser's runtime config from the container environment.
set -eu
cat > /usr/share/nginx/html/config.json <<EOF
{"temporalUiUrl": "${TEMPORAL_UI_URL:-}", "temporalNamespace": "${TEMPORAL_NAMESPACE:-collisions}"}
EOF
