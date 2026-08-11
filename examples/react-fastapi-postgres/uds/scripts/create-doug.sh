#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/../.."

KEYCLOAK_URL="https://keycloak.admin.uds.dev"
DOUG_USERNAME="doug"
DOUG_PASSWORD='unicorn123!@#UN'

# Read the development Keycloak administrator credentials created by UDS Core.
KEYCLOAK_ADMIN_USERNAME="$(
  uds zarf tools kubectl get secret keycloak-admin-password \
    --namespace keycloak \
    --output jsonpath='{.data.username}' \
    | base64 --decode
)"
KEYCLOAK_ADMIN_PASSWORD="$(
  uds zarf tools kubectl get secret keycloak-admin-password \
    --namespace keycloak \
    --output jsonpath='{.data.password}' \
    | base64 --decode
)"

# Exchange the administrator credentials for a Keycloak Admin API token.
KEYCLOAK_ADMIN_ACCESS_TOKEN="$(
  curl --fail --silent --show-error --location \
    "${KEYCLOAK_URL}/realms/master/protocol/openid-connect/token" \
    --header "Content-Type: application/x-www-form-urlencoded" \
    --data-urlencode "username=${KEYCLOAK_ADMIN_USERNAME}" \
    --data-urlencode "password=${KEYCLOAK_ADMIN_PASSWORD}" \
    --data-urlencode "client_id=admin-cli" \
    --data-urlencode "grant_type=password" \
    | uds zarf tools yq -er '.access_token'
)"

# Leave an existing Doug account unchanged so the script is safe to rerun.
EXISTING_USER_ID="$(
  curl --fail --silent --show-error --get \
    "${KEYCLOAK_URL}/admin/realms/uds/users" \
    --header "Authorization: Bearer ${KEYCLOAK_ADMIN_ACCESS_TOKEN}" \
    --data-urlencode "username=${DOUG_USERNAME}" \
    --data-urlencode "exact=true" \
    | uds zarf tools yq -r '.[0].id // ""'
)"

if [[ -n "$EXISTING_USER_ID" ]]; then
  echo "Doug already exists in the UDS realm. Nothing to do."
  exit 0
fi

# Create the standard UDS development user.
curl --fail --silent --show-error --output /dev/null --location \
  "${KEYCLOAK_URL}/admin/realms/uds/users" \
  --header "Content-Type: application/json" \
  --header "Authorization: Bearer ${KEYCLOAK_ADMIN_ACCESS_TOKEN}" \
  --data-raw '{
    "username": "doug",
    "firstName": "Doug",
    "lastName": "Unicorn",
    "email": "doug@uds.dev",
    "attributes": {
      "mattermostid": "1"
    },
    "emailVerified": true,
    "enabled": true,
    "requiredActions": [],
    "credentials": [
      {
        "type": "password",
        "value": "unicorn123!@#UN",
        "temporary": false
      }
    ]
  }'

# Match the uds-common development task by disabling its Conditional OTP step.
CONDITIONAL_OTP_ID="$(
  curl --fail --silent --show-error --location \
    "${KEYCLOAK_URL}/admin/realms/uds/authentication/flows/Authentication/executions" \
    --header "Authorization: Bearer ${KEYCLOAK_ADMIN_ACCESS_TOKEN}" \
    | uds zarf tools yq -er '.[] | select(.displayName == "Conditional OTP") | .id'
)"

curl --fail --silent --show-error --output /dev/null --location --request PUT \
  "${KEYCLOAK_URL}/admin/realms/uds/authentication/flows/Authentication/executions" \
  --header "Content-Type: application/json" \
  --header "Authorization: Bearer ${KEYCLOAK_ADMIN_ACCESS_TOKEN}" \
  --data "{
    \"id\": \"${CONDITIONAL_OTP_ID}\",
    \"requirement\": \"DISABLED\"
  }"

echo "Created Doug in the UDS realm."
echo "Username: ${DOUG_USERNAME}"
echo "Password: ${DOUG_PASSWORD}"
