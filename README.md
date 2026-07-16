# check-in-relay

The push relay for [Check-In](https://github.com/NC1107/check-in).

Check-In is open source, so anyone can spin up the server in Docker.
But push notifications are the one part a self-hoster cannot fully run themselves, and the reason is not their fault.
The apps published on the App Store and Google Play embed the maintainer's Firebase project, so every device mints its FCM token against that project.
A self-hoster pointing their server at their own Firebase credentials gets `SENDER_ID_MISMATCH` on every send while the server looks perfectly healthy.

The maintainer cannot just hand out the Firebase service account either: that credential can push to any Check-In device on any server, so it is a master key, not a per-host one.

This relay is the fix.
The maintainer runs one instance that holds the single credential.
A self-hosted Check-In server registers with it once on first boot, gets a scoped, revocable key, and from then on POSTs its notifications to the relay instead of straight to FCM.
So a host running the published apps gets working push, and nobody but the maintainer ever holds the credential.

## What the relay sees

Only what it needs to deliver: a short notification title and body, the device tokens to send to, and a small data payload (the notification type and a post id).
It never sees post content, photos, comments, phone numbers, or who is in a group.
It does not log tokens or notification text, only counts and error codes.

## How it works

1. A Check-In server boots with a relay URL configured (this is the default on the published image).
2. If it has no key yet, it calls `POST /v1/register` with its public URL. The relay mints a key (registration is per-IP rate-limited) and returns it once, recording the public URL as an admin label. The server stores the key and reuses it forever after.
3. When something happens worth a notification, the server calls `POST /v1/send` with its key and a batch of `{token, title, body, data}`. The relay forwards each to FCM and returns a per-token result so the server can prune tokens FCM reports as dead.

Keys are stored as SHA-256 hashes in a small SQLite file, so a leak of the database yields nothing usable.
Registration is rate-limited per IP and sending is rate-limited per key.
A misbehaving server can be cut off with `POST /admin/keys/{id}/revoke`.

## API

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| `POST` | `/v1/register` | none (IP rate-limited) | Mint a key. Optional body `{"publicUrl": "..."}` is recorded, unverified, as an admin label. |
| `POST` | `/v1/send` | `Authorization: Bearer <key>` | Forward `{"messages": [{"token","title","body","data"}]}` to FCM. Returns `{"results": [{"token","status"}]}` where status is `delivered`, `unregistered`, or `error`. |
| `GET` | `/healthz` | none | Liveness. |
| `GET` | `/admin/keys` | `Authorization: Bearer <admin token>` | List issued keys (metadata only). |
| `POST` | `/admin/keys/{id}/revoke` | `Authorization: Bearer <admin token>` | Revoke a key. |

Admin endpoints are only mounted when `RELAY_ADMIN_TOKEN` is set.

## Running it

You need the Firebase service-account JSON for the project the published apps are built against.

```bash
cp .env.example .env          # set RELAY_DOMAIN and RELAY_ADMIN_TOKEN
cp /path/to/service-account.json ./fcm-service-account.json
docker compose up -d
```

Caddy fetches a Let's Encrypt certificate for `RELAY_DOMAIN`, so point that subdomain's DNS at the host.
The `relay_data` volume holds the SQLite key store; back it up and losing it means every registered server re-registers on its next boot.

### Behind an existing reverse proxy

If you already run Traefik or another proxy, drop the `caddy` service from `docker-compose.yml` and route your proxy to the relay container on port `8090`.
The relay reads the client IP from `X-Forwarded-For`, which a trusted proxy sets, so per-IP registration limits stay accurate.

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `RELAY_HTTP_ADDR` | `:8090` | Listen address. |
| `RELAY_FCM_CREDENTIALS_FILE` | *(required)* | Path inside the container to the Firebase service-account JSON. |
| `RELAY_DB_PATH` | `/data/relay.db` | SQLite key store path. |
| `RELAY_ADMIN_TOKEN` | *(empty)* | Guards `/admin`. Empty disables those endpoints. |
| `RELAY_REGISTER_PER_HOUR` | `5` | Per-IP registration rate. |
| `RELAY_REGISTER_BURST` | `3` | Per-IP registration burst. |
| `RELAY_SEND_PER_MINUTE` | `120` | Per-key send rate. |
| `RELAY_SEND_BURST` | `60` | Per-key send burst. |
| `RELAY_MAX_MESSAGES` | `500` | Device tokens allowed in one `/v1/send`. |

## Cost

FCM costs nothing at any volume, so this relay is cheap to run and will not paywall any feature for as long as that stays true.
If the hosting ever becomes a real recurring cost, that will be stated plainly rather than quietly turned into a subscription.

## Development

```bash
go test ./...
go build ./cmd/relay
```

Go 1.26, no CGO (the SQLite driver is pure Go), single static binary.
