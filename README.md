# Szorty

Short links for `https://szorty.infinigy.pl`. One container on the Docker network `traefik`. The host port is not published.

## Deploy

Directory on the server: `/opt/www/szorty.infinigy.pl`.

DNS: add `szorty.infinigy.pl` in Cloudflare the same way as `s4cred.infinigy.pl`.

Google OAuth redirect: `https://szorty.infinigy.pl/auth/callback`.

Copy `.env.example` to `.env` and fill in the four names. `PUBLIC_BASE_URL`, `DB_PATH`, and `LISTEN_ADDR` come from `compose.yaml`.

```bash
mkdir -p data && chown 65532:65532 data
docker compose up -d --build
```

Do not run `docker compose down -v`. Deleting `./data` deletes the links.

A database copy is `./data/szort.db` together with the `-wal` and `-shm` files, taken while the container is stopped.

## Dev machine

The OAuth redirect `http://127.0.0.1:8080/auth/callback` is only for a dev machine.
