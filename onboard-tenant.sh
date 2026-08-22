#!/usr/bin/env bash
#
# onboard-tenant.sh — per-tenant onboarding script
# docs/tenant-provisioning/design.md §4.2, §6
#
# Creates one tenant's Keycloak user, NATS Account + User, JetStream KV
# bucket, pushes the Account/User JWTs to the NATS resolver, and writes the
# tenant's NATS user JWT and seed to Azure Key Vault.
#
# Must run on the operator's own workstation — never in-cluster — since it
# needs the nsc Operator identity that can mint a new Account
# (docs/nats-cluster/design.md §4) plus Keycloak realm-admin credentials and
# a Key Vault-write-capable Azure AD identity (§8).
#
# Prerequisites (assumed already done, out of scope here):
#   - docs/nats-cluster/design.md §4: Operator/SYS-account bootstrap
#   - docs/keycloak-operator/design.md: Keycloak deployed and reachable
#   - docs/tenant-provisioning/design.md §4.1: one-time realm/client/
#     protocol-mapper setup in Keycloak (the natssaas realm, rest-api client,
#     tenant client scope) — run once, ever, separately from this script
#
# Usage:
#   ./onboard-tenant.sh <tenant_id> <admin_email>
#
# Required environment variables:
#   KEYCLOAK_URL             Keycloak base URL, e.g. https://auth.natssaas.example.com/auth
#   KEYCLOAK_ADMIN_USER      realm-admin username for kcadm.sh
#   KEYCLOAK_ADMIN_PASSWORD  realm-admin password for kcadm.sh
#   NATS_URL                 NATS server URL reachable from this workstation,
#                             e.g. nats://nats.natssaas.example.com:4222
#   KEY_VAULT_NAME            target Azure Key Vault name (docs/terraform-infra/design.md output)
#
# Optional environment variables:
#   KEYCLOAK_ADMIN_REALM  realm kcadm.sh authenticates against (default: master)
#   KEYCLOAK_REALM        realm the tenant user is created in (default: natssaas)
#   KV_MAX_BYTES          JetStream KV bucket quota in bytes (default: 1073741824 — 1 GiB,
#                          docs/nats-tenant-queue-api/design.md §12)
#
# Running this script twice in a row with the same tenant_id is a safe
# no-op for whichever steps already succeeded (INV-2) — this is how
# onboarding resumes after any partial failure (§7).

set -euo pipefail

# ---------------------------------------------------------------------------
# Arguments and naming rule (§6)
# ---------------------------------------------------------------------------

if [[ $# -ne 2 ]]; then
  echo "Usage: $0 <tenant_id> <admin_email>" >&2
  exit 1
fi

TENANT_ID="$1"
ADMIN_EMAIL="$2"

TENANT_ID_RE='^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
if [[ ! "$TENANT_ID" =~ $TENANT_ID_RE ]]; then
  echo "tenant_id '${TENANT_ID}' fails the naming rule (${TENANT_ID_RE})." >&2
  echo "No Keycloak, NATS, or Key Vault operation was performed." >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Required tooling and configuration
# ---------------------------------------------------------------------------

for bin in kcadm.sh nsc nats az; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "Required tool '${bin}' not found on PATH." >&2
    exit 1
  fi
done

: "${KEYCLOAK_URL:?KEYCLOAK_URL must be set}"
: "${KEYCLOAK_ADMIN_USER:?KEYCLOAK_ADMIN_USER must be set}"
: "${KEYCLOAK_ADMIN_PASSWORD:?KEYCLOAK_ADMIN_PASSWORD must be set}"
: "${NATS_URL:?NATS_URL must be set}"
: "${KEY_VAULT_NAME:?KEY_VAULT_NAME must be set}"

KEYCLOAK_ADMIN_REALM="${KEYCLOAK_ADMIN_REALM:-master}"
KEYCLOAK_REALM="${KEYCLOAK_REALM:-natssaas}"
KV_MAX_BYTES="${KV_MAX_BYTES:-1073741824}"

NATS_JWT_SECRET="tenant-${TENANT_ID}-nats-jwt"
NATS_SEED_SECRET="tenant-${TENANT_ID}-nats-seed"

TENANT_CREDS_FILE="$(mktemp)"
trap 'rm -f "${TENANT_CREDS_FILE}"' EXIT

step() { echo "[$1/5] $2"; }
ok()   { echo "      -> $1"; }
fail() { echo "      -> FAILED: $1" >&2; exit 1; }

kcadm() {
  kcadm.sh "$@" --server "${KEYCLOAK_URL}" --realm "${KEYCLOAK_ADMIN_REALM}" \
    --user "${KEYCLOAK_ADMIN_USER}" --password "${KEYCLOAK_ADMIN_PASSWORD}"
}

# ---------------------------------------------------------------------------
# Step 1 — Keycloak user (§4.2)
# ---------------------------------------------------------------------------

step 1 "Keycloak user (${ADMIN_EMAIL}, tenant_id=${TENANT_ID})"

EXISTING_USER_ID="$(kcadm get users -r "${KEYCLOAK_REALM}" -q "username=${ADMIN_EMAIL}" \
  --fields id --format csv --noquotes 2>/dev/null | head -n1 || true)"

if [[ -n "${EXISTING_USER_ID}" ]]; then
  ok "already exists (id=${EXISTING_USER_ID}), skipping"
else
  TEMP_PASSWORD="$(openssl rand -base64 24)"
  NEW_USER_ID="$(kcadm create users -r "${KEYCLOAK_REALM}" \
    -s "username=${ADMIN_EMAIL}" \
    -s "email=${ADMIN_EMAIL}" \
    -s enabled=true \
    -s emailVerified=false \
    -s "attributes.tenant_id=${TENANT_ID}" \
    -s 'requiredActions=["UPDATE_PASSWORD"]' \
    -i)" || fail "Keycloak user creation"
  kcadm set-password -r "${KEYCLOAK_REALM}" --userid "${NEW_USER_ID}" \
    --new-password "${TEMP_PASSWORD}" --temporary || fail "Keycloak temporary password"
  ok "created (id=${NEW_USER_ID}); temporary password set, must reset on first login"
  echo "      temporary password: ${TEMP_PASSWORD}"
  echo "      (deliver this out-of-band — see docs/tenant-provisioning/design.md §12 Open questions)"
fi

# ---------------------------------------------------------------------------
# Step 2 — NATS Account + User (§4.2, §6)
# ---------------------------------------------------------------------------

step 2 "NATS Account and User"

if nsc describe account "${TENANT_ID}" >/dev/null 2>&1; then
  ok "Account '${TENANT_ID}' already exists, skipping"
else
  nsc add account "${TENANT_ID}" >/dev/null || fail "nsc add account"
  ok "Account '${TENANT_ID}' created"
fi

if nsc describe user -a "${TENANT_ID}" api >/dev/null 2>&1; then
  ok "User 'api' already exists under account '${TENANT_ID}', skipping"
else
  nsc add user -a "${TENANT_ID}" api >/dev/null || fail "nsc add user"
  ok "User 'api' created under account '${TENANT_ID}'"
fi

nsc generate creds -a "${TENANT_ID}" -n api >"${TENANT_CREDS_FILE}" || fail "nsc generate creds"

# ---------------------------------------------------------------------------
# Step 3 — JetStream KV bucket (§4.2, §6)
# ---------------------------------------------------------------------------

step 3 "JetStream KV bucket '${TENANT_ID}'"

if nats kv info "${TENANT_ID}" --server "${NATS_URL}" --creds "${TENANT_CREDS_FILE}" >/dev/null 2>&1; then
  ok "bucket already exists, skipping"
else
  nats kv add "${TENANT_ID}" \
    --history=1 \
    --max-bytes="${KV_MAX_BYTES}" \
    --server "${NATS_URL}" \
    --creds "${TENANT_CREDS_FILE}" >/dev/null || fail "nats kv add"
  ok "bucket created (history=1, max-bytes=${KV_MAX_BYTES})"
fi

# ---------------------------------------------------------------------------
# Step 4 — Push Account/User JWTs to the resolver (§4.2, §6)
# ---------------------------------------------------------------------------

step 4 "Push Account/User JWTs to the NATS resolver"

nsc push -a "${TENANT_ID}" -u "${NATS_URL}" >/dev/null || \
  fail "nsc push (Account exists locally but is not yet accepted by the resolver — safe to re-run)"
ok "pushed"

# ---------------------------------------------------------------------------
# Step 5 — Write NATS user JWT and seed to Key Vault (§4.2, §6)
# ---------------------------------------------------------------------------

step 5 "Key Vault write (${NATS_JWT_SECRET}, ${NATS_SEED_SECRET})"

USER_JWT="$(sed -n '/-----BEGIN NATS USER JWT-----/,/------END NATS USER JWT------/p' "${TENANT_CREDS_FILE}" | sed '1d;$d')"
USER_SEED="$(sed -n '/-----BEGIN USER NKEY SEED-----/,/------END USER NKEY SEED------/p' "${TENANT_CREDS_FILE}" | sed '1d;$d')"

if [[ -z "${USER_JWT}" || -z "${USER_SEED}" ]]; then
  fail "could not extract JWT/seed from generated creds — NATS provisioning complete, Key Vault write not attempted"
fi

JWT_EXISTS="$(az keyvault secret show --vault-name "${KEY_VAULT_NAME}" --name "${NATS_JWT_SECRET}" \
  --query id -o tsv 2>/dev/null || true)"
SEED_EXISTS="$(az keyvault secret show --vault-name "${KEY_VAULT_NAME}" --name "${NATS_SEED_SECRET}" \
  --query id -o tsv 2>/dev/null || true)"

if [[ -n "${JWT_EXISTS}" && -n "${SEED_EXISTS}" ]]; then
  ok "both secrets already exist, skipping write"
else
  if [[ -z "${JWT_EXISTS}" ]]; then
    az keyvault secret set --vault-name "${KEY_VAULT_NAME}" --name "${NATS_JWT_SECRET}" \
      --value "${USER_JWT}" >/dev/null || \
      fail "NATS provisioning complete, Key Vault write failed, re-run to retry"
  fi
  if [[ -z "${SEED_EXISTS}" ]]; then
    az keyvault secret set --vault-name "${KEY_VAULT_NAME}" --name "${NATS_SEED_SECRET}" \
      --value "${USER_SEED}" >/dev/null || \
      fail "NATS provisioning complete, Key Vault write failed, re-run to retry"
  fi
  ok "written"
fi

echo
echo "tenant '${TENANT_ID}' fully provisioned."
