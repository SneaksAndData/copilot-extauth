#!/usr/bin/env bash
# Verifies auth behaviour and a real completion through the gateway. Needs ./port-forward.sh running.
set -uo pipefail
cd "$(dirname "$0")"; source ./env.sh
URL="http://localhost:$LOCAL_PORT/v1/chat/completions"
MODEL="${MODEL:-gpt-4.1}"
BODY='{"model":"'"$MODEL"'","max_tokens":10,"messages":[{"role":"user","content":"Reply with exactly: pong"}]}'
fail=0
expect() { # name expected_code actual_code
  if [ "$2" = "$3" ]; then echo "PASS  $1 ($3)"; else echo "FAIL  $1: expected $2, got $3"; fail=1; fi; }
req() { curl -s -o /tmp/copilot-e2e.out -w '%{http_code}' "$URL" -H 'content-type: application/json' "$@" -d "$BODY"; }

expect "no token -> 401"          401 "$(req)"
expect "invalid token -> 403"     403 "$(req -H 'Authorization: Bearer gho_invalid')"
code=$(req -H "Authorization: Bearer $(gh auth token)")
expect "gh token (Authorization)" 200 "$code"; [ "$code" = 200 ] && echo "      reply: $(python3 -c 'import json;print(json.load(open("/tmp/copilot-e2e.out"))["choices"][0]["message"]["content"])')" || cat /tmp/copilot-e2e.out
expect "gh token (x-github-token)" 200 "$(req -H "x-github-token: $(gh auth token)")"
code=$(curl -sN -o /tmp/copilot-e2e.out -w '%{http_code}' "$URL" -H 'content-type: application/json' \
  -H "Authorization: Bearer $(gh auth token)" -d "${BODY%\}},\"stream\":true}")
expect "streaming" 200 "$code"; grep -q '^data: \[DONE\]' /tmp/copilot-e2e.out || { echo "FAIL  stream missing [DONE]"; fail=1; }
code=$(python3 -c "import json;print(json.dumps({'model':'$MODEL','max_tokens':5,'messages':[{'role':'system','content':'x '*60000},{'role':'user','content':'Reply: pong'}]}))" \
  | curl -s -o /dev/null -w '%{http_code}' "$URL" -H 'content-type: application/json' -H "Authorization: Bearer $(gh auth token)" -d @-)
expect "large (~120KB) request" 200 "$code"
rm -f /tmp/copilot-e2e.out
exit $fail
