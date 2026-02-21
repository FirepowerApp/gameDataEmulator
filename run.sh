#!/bin/bash
# Base execution script for mockdataserver
# This script pulls the latest container image and runs it with passed-through flags

set -e

# Configuration
IMAGE="${DOCKER_IMAGE:-ghcr.io/firepowerapp/firepowermockdataserver:latest}"
FORCE_PULL=false

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Parse script-specific flags
CONTAINER_ARGS=()
while [[ $# -gt 0 ]]; do
    case $1 in
        --force-pull)
            FORCE_PULL=true
            shift
            ;;
        *)
            CONTAINER_ARGS+=("$1")
            shift
            ;;
    esac
done

# Check if Docker is installed
if ! command -v docker &> /dev/null; then
    log_error "Docker is not installed. Please install Docker to use this script."
    exit 1
fi

# Check if Docker daemon is running
if ! docker info &> /dev/null; then
    log_error "Docker daemon is not running. Please start Docker."
    exit 1
fi

# Function to check if image exists locally
check_local_image() {
    docker image inspect "${IMAGE}" &> /dev/null
}

log_info "Pulling latest container image: ${IMAGE}"

# Pull the latest image and capture error output
PULL_SUCCESS=false
PULL_ERROR=$(docker pull "${IMAGE}" 2>&1)
PULL_EXIT_CODE=$?

if [ $PULL_EXIT_CODE -eq 0 ]; then
    log_info "Successfully pulled image: ${IMAGE}"
    PULL_SUCCESS=true
else
    # Analyze the error to determine if it's network-related
    IS_NETWORK_ERROR=false

    # Check for common network error patterns
    if echo "$PULL_ERROR" | grep -qiE '(dial tcp|connection refused|timeout|network unreachable|no route to host|temporary failure|name resolution|failed to resolve)'; then
        IS_NETWORK_ERROR=true
        log_warn "Network error while pulling image from registry"
    elif echo "$PULL_ERROR" | grep -qiE '(unauthorized|authentication required|denied|forbidden)'; then
        log_error "Authentication failed when pulling image"
        log_error "Make sure you are authenticated to GitHub Container Registry:"
        log_error "  docker login ghcr.io"
        exit 1
    elif echo "$PULL_ERROR" | grep -qiE '(not found|manifest unknown)'; then
        log_error "Image not found in registry: ${IMAGE}"
        log_error "Please verify the image name is correct"
        exit 1
    else
        log_warn "Failed to pull image from registry"
        log_warn "Error: ${PULL_ERROR}"
    fi

    # Only attempt fallback for network errors (or unclassified errors if not force pull)
    if [ "$FORCE_PULL" = true ]; then
        log_error "Force pull mode enabled - failing due to registry pull failure"
        exit 1
    fi

    if [ "$IS_NETWORK_ERROR" = true ] || [ "$FORCE_PULL" = false ]; then
        # Try to use local image
        if check_local_image; then
            log_warn "Using locally cached image: ${IMAGE}"
            log_warn "This may not be the latest version"
        else
            log_error "No local image found: ${IMAGE}"
            log_error "Unable to pull from registry and no cached image available"
            exit 1
        fi
    else
        # Non-network error in non-force mode - still fail
        exit 1
    fi
fi

log_info "Running container with provided flags: ${CONTAINER_ARGS[*]}"

# Run the container with all passed-through arguments
RUN_ARGS=()

# Expose the server ports
RUN_ARGS+=(-p "8124:8124")  # Stats server port
RUN_ARGS+=(-p "8125:8125")  # Play-by-play server port

# Set timezone to host timezone
RUN_ARGS+=(-e "TZ=$(cat /etc/timezone 2>/dev/null || echo 'UTC')")

# Set default environment variables if not already set
if [[ ! "${CONTAINER_ARGS[*]}" =~ PLAYBYPLAY_PORT ]]; then
    RUN_ARGS+=(-e "PLAYBYPLAY_PORT=8125")
fi

if [[ ! "${CONTAINER_ARGS[*]}" =~ STATS_PORT ]]; then
    RUN_ARGS+=(-e "STATS_PORT=8124")
fi

# Run the container
docker run --rm \
    "${RUN_ARGS[@]}" \
    "${IMAGE}" \
    "${CONTAINER_ARGS[@]}"

log_info "Container execution completed"
