#!/usr/bin/env bash
# Starts Copilot CLI with the model provider pointed at the local Agent Router (BYOK mode).
# The provider API key is your GitHub token; the gateway turns it into your Copilot credential.
# Usage: ./copilot.sh [copilot args...]   e.g. ./copilot.sh -p "say hi"   MODEL=claude-opus-5.5 ./copilot.sh
# MODEL must support Copilot's /chat/completions endpoint (Claude models do; GPT-5.x are /responses-only).
set -euo pipefail
source "$(dirname "$0")/env.sh"  # no cd: copilot runs in your current directory
export COPILOT_PROVIDER_TYPE=openai
export COPILOT_PROVIDER_BASE_URL="http://localhost:$LOCAL_PORT/v1"
# Fetched per request, so a refreshed `gh` login is picked up without restarting.
export COPILOT_PROVIDER_API_KEY_COMMAND="${COPILOT_PROVIDER_API_KEY_COMMAND:-gh auth token}"
export COPILOT_MODEL="${MODEL:-claude-sonnet-5}"
exec copilot "$@"
