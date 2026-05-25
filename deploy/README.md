# Deploy

Target: existing Hetzner CPX22 (`ssh hetzner-odoo`), shared with the Odoo
test instance. See [ADR-002](../docs/adr/0002-m0-deployment.md) for the
rationale behind the choices below.

## First-time setup on the Hetzner host

```bash
# 1. Clone the repo on the host
ssh hetzner-odoo
mkdir -p ~/stele && cd ~/stele
git clone <repo-url> .

# 2. Create deploy/.env from the template
cp deploy/.env.example deploy/.env
$EDITOR deploy/.env   # set STELE_DB_PASSWORD to `openssl rand -base64 24`

# 3. Build and start the stack
cd deploy
docker compose build
docker compose up -d

# 4. Wire Caddy: append the snippet to the existing Odoo Caddyfile
cat ~/stele/deploy/Caddyfile.snippet >> ~/odoo/caddy/Caddyfile
cd ~/odoo && docker compose exec caddy caddy validate --config /etc/caddy/Caddyfile
docker compose restart caddy

# 5. Verify
curl -sS https://stele.178-105-44-164.sslip.io/healthz
```

## Update an existing deployment

```bash
ssh hetzner-odoo
cd ~/stele
git pull
cd deploy
docker compose build app
docker compose up -d app
```

## Rollback

Caddy: `git checkout` the Caddyfile change in `~/odoo/caddy/Caddyfile`,
then `docker compose restart caddy` from `~/odoo`.

App: `git checkout <previous-sha>` in `~/stele`, then rebuild + up.

## Backups

Not yet implemented. M0 has no user data. First migration lands at M1;
nightly `pg_dump` of `stele` db is on the M1 acceptance criteria list.
