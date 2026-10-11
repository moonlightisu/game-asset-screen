#!/bin/sh
set -eu

: "${ASSET_PATH:?set ASSET_PATH to a JPEG or PNG}"

curl --fail-with-body \
  --request POST \
  --header "Idempotency-Key: match-42-player-7" \
  --form "caption=Victory pose after the final round" \
  --form "image=@${ASSET_PATH}" \
  http://localhost:8080/submissions
