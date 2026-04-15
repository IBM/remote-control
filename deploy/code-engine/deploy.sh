#!/usr/bin/env bash

# Run from project root
cd $(dirname ${BASH_SOURCE[0]})/../..

# Pull in config from .env if present
if [ -f .env ]; then
    source .env
fi

# Log function
log() { echo "▶ $*"; }

# ── Configuration ─────────────────────────────────────────────────────────────
APP_NAME=${APP_NAME:-"remote-control"}
PROJECT_NAME=${PROJECT_NAME:-${APP_NAME}}
RESOURCE_GROUP=${RESOURCE_GROUP:-${APP_NAME}}
CR_REGION=${CR_REGION:-"us-south"}
CR_NAMESPACE=${CR_NAMESPACE:-"remote-control-ghart"}
IMAGE_TAG=${IMAGE_TAG:-"$(./scripts/version.sh)"}
IMAGE=${IMAGE:-"us.icr.io/${CR_NAMESPACE}/${APP_NAME}:${IMAGE_TAG}"}

IBM_API_KEY=${IBM_API_KEY:-""}
ACCOUNT=${ACCOUNT:-""}

PORT=8443
MIN_SCALE=0
MAX_SCALE=1
SCALE_DOWN_DELAY=120 # seconds; tune to outlive typical WS sessions
REQUEST_TIMEOUT=600  # seconds; Code Engine maximum
CONCURRENCY=100
CPU="0.5"
MEMORY="1G"

# Check mTLS config before proceeding
REMOTE_CONTROL_HOME=${REMOTE_CONTROL_HOME:-"$HOME/.remote-control"}
tls_files=(
    $REMOTE_CONTROL_HOME/ca-client.crt
    $REMOTE_CONTROL_HOME/server.crt
    $REMOTE_CONTROL_HOME/server.key
)
for tls_file in ${tls_files[@]}; do
    if [ ! -f $tls_file ]; then
        log 'ERROR: No TLS config found. Please run `remote-control init`'
        exit 1
    fi
done
config_file="${REMOTE_CONTROL_HOME}/config.json"
if [ ! -f "$config_file" ]; then
    log 'ERROR: no config.json found. Please run `remote-control init`'
    exit 1
fi

# ── 1. Authenticate ───────────────────────────────────────────────────────────
log "Logging in to IBM Cloud"
account_arg=""
if [ "${ACCOUNT}" != "" ]; then
    log "Targeting account ${ACCOUNT}"
    account_arg="-c ${ACCOUNT}"
fi
if [ "${IBM_API_KEY}" != "" ]; then
    ibmcloud login --apikey "${IBM_API_KEY}" -q ${account_arg}
else
    ibmcloud login --sso ${account_arg}
fi

log "Logging in to Container Registry (${CR_REGION})"
ibmcloud cr region-set "${CR_REGION}"
ibmcloud cr login

# ── 2. Ensure CR namespace and resource group exist ───────────────────────────
log "Ensuring Resource Group: ${RESOURCE_GROUP}"
ibmcloud resource group-add "${RESOURCE_GROUP}" 2>/dev/null || true
ibmcloud target -g ${RESOURCE_GROUP}

log "Ensuring CR namespace: ${CR_NAMESPACE}"
ibmcloud cr namespace-add "${CR_NAMESPACE}" 2>/dev/null || true

# ── 3. Build and push image ───────────────────────────────────────────────────
log "Building Docker image"
IMAGE_NAME=$IMAGE make docker.release

# ── 4. Code Engine project ────────────────────────────────────────────────────
log "Selecting or creating Code Engine project: ${PROJECT_NAME}"
if ! ibmcloud ce project select --name "${PROJECT_NAME}" -q 2>/dev/null; then
    ibmcloud ce project create --name "${PROJECT_NAME}"
    ibmcloud ce project select --name "${PROJECT_NAME}"
fi

# ── 5. API Key ────────────────────────────────────────────────────────────────
if [ "${IBM_API_KEY}" == "" ]; then
    project_api_key_name="${APP_NAME}-registry"
    if ! ibmcloud iam api-key ${project_api_key_name} &>/dev/null; then
        log "Creating new API Key: $project_api_key_name"
        expiry=$(date -v+1y +"%Y-%m-%dT%H:%M%z" 2>/dev/null || date -d "+1 year" +"%Y-%m-%dT%H:%M%z") # MacOS or linux
        key_value=$(ibmcloud iam api-key-create ${project_api_key_name} -d "API Key for ${APP_NAME} project" -e "${expiry}" | tail -n1 | sed 's,API Key *,,g')
        IBM_API_KEY=${key_value}
        echo "IBM_API_KEY=${key_value}" >> .env
    fi
fi

# ── 6. Registry secret ────────────────────────────────────────────────────────
REGISTRY_SECRET="${APP_NAME}-registry"
log "Creating/updating registry secret: ${REGISTRY_SECRET}"
ibmcloud ce registry delete --name "${REGISTRY_SECRET}" --force 2>/dev/null || true
ibmcloud ce registry create \
    --name "${REGISTRY_SECRET}" \
    --server "us.icr.io" \
    --username iamapikey \
    --password "${IBM_API_KEY}"

# ── 7. Config secret ──────────────────────────────────────────────────────────
CONFIG_SECRET="${APP_NAME}-config"
log "Creating/updating config secret: ${CONFIG_SECRET}"
create_args=""
for tls_file in "${tls_files[@]}"; do
    create_args="${create_args} --from-file $(basename ${tls_file})=${tls_file}"
done
cat $REMOTE_CONTROL_HOME/config.json \
    | sed "s,$REMOTE_CONTROL_HOME,/home/app/.remote-control,g" \
    | sed 's/"require_approval": false/"require_approval": true/g' > deploy_config.json
create_args="$create_args --from-file config.json=deploy_config.json"

ibmcloud ce secret delete --name "${CONFIG_SECRET}" --force 2>/dev/null || true
ibmcloud ce secret create --name "${CONFIG_SECRET}" ${create_args}
rm deploy_config.json


# ── 8. Deploy application ─────────────────────────────────────────────────────
DEPLOY_ARGS=(
    --name             "${APP_NAME}"
    --image            "${IMAGE}"
    --registry-secret  "${REGISTRY_SECRET}"
    --port             "${PORT}"
    --min-scale        "${MIN_SCALE}"
    --max-scale        "${MAX_SCALE}"
    --scale-down-delay "${SCALE_DOWN_DELAY}"
    --request-timeout  "${REQUEST_TIMEOUT}"
    --concurrency      "${CONCURRENCY}"
    --cpu              "${CPU}"
    --memory           "${MEMORY}"
    --mount-secret     "/home/app/.remote-control=${CONFIG_SECRET}"
)

log "Deploying application: ${APP_NAME}"
if ibmcloud ce application get --name "${APP_NAME}" &>/dev/null; then
    log "Application exists — updating"
    ibmcloud ce application update "${DEPLOY_ARGS[@]}"
else
    log "Application does not exist — creating"
    ibmcloud ce application create "${DEPLOY_ARGS[@]}"
fi

# ── 9. Wait for rollout ───────────────────────────────────────────────────────
log "Waiting for application to become ready"
ibmcloud ce application get --name "${APP_NAME}"
