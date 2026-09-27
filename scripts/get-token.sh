#!/bin/bash
# ==============================================================================
# Helper script to obtain Keycloak OAuth2 / OIDC Bearer Tokens
# Usage: ./scripts/get-token.sh [provider-a | provider-b | internal-service]
# ==============================================================================
set -euo pipefail

CLIENT_TYPE="${1:-provider-a}"
KEYCLOAK_URL="${KEYCLOAK_BASE_URL:-http://localhost:8082}"
REALM="${KEYCLOAK_REALM:-wager-realm}"

CLIENT_ID=""
CLIENT_SECRET=""

case "${CLIENT_TYPE}" in
  "internal-service"|"internal")
    CLIENT_ID="internal-service"
    CLIENT_SECRET="internal-secret-123"
    ;;
  "provider-a")
    CLIENT_ID="provider-a"
    CLIENT_SECRET="provider-a-secret-123"
    ;;
  "provider-b")
    CLIENT_ID="provider-b"
    CLIENT_SECRET="provider-b-secret-123"
    ;;
  *)
    echo "Unknown client type: ${CLIENT_TYPE}"
    echo "Valid options: internal-service, provider-a, provider-b"
    exit 1
    ;;
esac

TOKEN_ENDPOINT="${KEYCLOAK_URL}/realms/${REALM}/protocol/openid-connect/token"

RESPONSE=$(curl -s -X POST "${TOKEN_ENDPOINT}" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "grant_type=client_credentials" \
  -d "client_id=${CLIENT_ID}" \
  -d "client_secret=${CLIENT_SECRET}")

ACCESS_TOKEN=$(echo "${RESPONSE}" | grep -o '"access_token":"[^"]*' | cut -d'"' -f4 || true)

if [ -z "${ACCESS_TOKEN}" ]; then
  echo "Error retrieving token:"
  echo "${RESPONSE}"
  exit 1
fi

echo "${ACCESS_TOKEN}"
