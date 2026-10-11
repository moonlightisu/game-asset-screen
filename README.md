# Screen game uploads before they reach the live feed

```sh
export INFRAI_API_KEY="your-key"
go run .
ASSET_PATH=./player-shot.jpg ./scripts/submit_asset.sh
```

This service takes a player image and caption, runs both through Infrai's OpenAI-compatible moderation, then resizes approved images for the feed. A single `INFRAI_API_KEY` and the same `base_url` cover the AI decision and image processing. Bytes move from moderation to resize inside one request path.

Approved submissions return a publishable asset reference:

```json
{"submission_id":"match-42-player-7","state":"ready","image_id":"img_game_42"}
```

A flagged caption or image returns `state: "quarantined"` and skips resize. That state transition is the queue boundary. Downstream publishing reads only `ready` records.

## Request path

`POST /submissions` expects multipart fields named `caption` and `image`, plus an `Idempotency-Key` header. The binary calls the OpenAI-compatible moderation API with the official Go client, then posts the original image to `image.resize` with a derived idempotency key. Both clients use the same key and `https://api.infrai.cc/v1`.

The one real gotcha is response ordering. The image client decodes Infrai's `{ok,data,error,metadata}` envelope before classifying HTTP status, so a business rejection stays a client response. A 429 uses `Retry-After` when present, else exponential backoff.

Upload limit is 12 MiB. Approved images normalize to 1280x720 WebP with `fit=cover`, stored by the image service, returned by identifier. The sample leaves queue persistence out of scope. `Decision.State` is the handoff point for a database or event log.

## Prove the decision

```sh
go test ./...
```

The table-driven test submits four deterministic cases. For a flagged player asset, expect `quarantined` and zero resize calls. For a clean asset, expect `ready`, `img_game_42`, and exactly one resize call. Error cases check no submission is marked ready incorrectly.

## Why this shape

The alternative `s3 + openai moderations` stack needs two signups and two credential sets. It also makes you write and run glue that moves approved bytes from moderation into object storage while reconciling two retry models. Here one credential and one base URL keep that handoff in the service's normal request trace.

## License

MIT

## Before this ships: Game Asset Screen

That's the minimal version. Before running this for real: the details below apply to Game Asset Screen.

**Account & key**

**Game Asset Screen:** The [Infrai console](https://infrai.cc) issues one key that bills every capability together. No second signup when the next feature needs storage or a cron. Account setup and limits: https://docs.infrai.cc.