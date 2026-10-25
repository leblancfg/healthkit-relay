#!/usr/bin/env bash
# Deploys the demo server Worker (README.md). Needs CLOUDFLARE_API_TOKEN (Workers Scripts,
# Workers Routes and DNS edit on leblancfg.com).
set -euo pipefail
cd "$(dirname "$0")"
version="$(sed -n 's/.*version = "\([0-9.]*\)+.*/\1/p' ../../flake.nix)+worker.$(git rev-parse --short HEAD)"
npm ci --no-audit --no-fund
npx wrangler deploy --var "VERSION:$version"
