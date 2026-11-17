#!/usr/bin/env bash
# Existing V3.1.13 upgrade build sequence (deploy/update-source.sh L284-L296).
# The test supplies local command doubles; no network, database or service calls.
set -Eeuo pipefail
cd "$PROJECT_DIR/frontend"
npm ci --registry=https://registry.npmjs.org
npm run build
cd "$PROJECT_DIR/backend"
go build -o "$PROJECT_DIR/transithub-api.next" ./cmd/api
test -x "$PROJECT_DIR/transithub-api.next"
