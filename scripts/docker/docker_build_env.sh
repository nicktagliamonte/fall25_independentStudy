#!/usr/bin/env bash
# Purpose: Default env for docker-compose / BuildKit builds so long-running steps (go mod
# download, large layers) are not cancelled by short client timeouts.

export COMPOSE_HTTP_TIMEOUT="${COMPOSE_HTTP_TIMEOUT:-86400}"
export DOCKER_BUILDKIT="${DOCKER_BUILDKIT:-1}"
