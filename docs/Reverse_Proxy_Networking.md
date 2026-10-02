# Reverse proxy networking

How to put a proxy in front of KyMessages so the server sees each visitor's real
address. Applies to `docker-compose.proxy.yml`.

## Why the proxy shares the app's network

Login and other IP-keyed lockouts, and the addresses in audit rows, depend on the
peer address being the caller's. Traffic that crosses from one Docker network to
another, or loops back through a published port, is source-NATed to a bridge
gateway (`10.x.0.1`) that is **the same for every caller**. Every lockout then
shares one bucket: enough failures from anyone lock out everyone.

So the proxy joins `kymessages-net` and reaches the app by name. Container-to-
container traffic ignores published ports; the overlay publishes none.

Each app has its own network (`kypost-net`, `kymessages-net`), so apps cannot
reach each other. Only the proxy is on both.

## Setup

1. Append `docker-compose.proxy.yml` to `COMPOSE_FILE` in `.env`, keeping existing
   overlays (build, LAN-DNS, static-IP). Never replace the chain with `-f`.
2. Set `KY_APP_URL=https://<your host>`, `KY_SESSION_SECRET` and
   `KY_TRUSTED_PROXIES=<proxy address>/32`, the proxy's pinned address on
   `kymessages-net`. The overlay refuses to start without all three. Generate the
   secret once with `openssl rand -hex 32` and keep it in `.env`; it must stay the
   same across restarts. It signs the password-login proof-of-work challenges, so a new
   secret rejects every challenge issued before the restart and those sign-ins must
   be retried. It does not sign sessions or encrypt data. An `http://` `KY_APP_URL`
   is refused at startup and the container keeps restarting; `docker compose logs`
   shows the reason. `KY_TRUSTED_PROXIES` is exactly the proxy's own /32. Never the
   network's subnet: it includes the gateway 10.91.0.1 and every container on the
   network, and any of them could then forge client addresses.
3. Bring KyMessages up first: it creates and owns the network. The overlay also
   sets `KY_ENV=production`.

The overlay names the network `kymessages-net` (`KY_NETWORK` overrides) with subnet
`10.91.0.0/24` (`KY_NETWORK_SUBNET` overrides). Pinned addresses need a
user-configured subnet; Docker rejects them on an auto-configured network.

### With the static-IP overlay

`docker-compose.static-ip.yml` also sets the network's subnet from the required
`KY_NETWORK_SUBNET`. Compose merges both `ipam` entries into one as long as they
agree, and they do because both read that variable. `scripts/check-compose-proxy.sh`
proves it. Keep `KY_CONTAINER_IP` inside the subnet, and pin the proxy high or low
away from it: with the defaults, the app at `10.91.0.20` and cloudflared at
`10.91.0.10`. Keep the network to just those two, or Docker may hand a pinned
address to a third container.

## cloudflared

cloudflared is typically already running for another app. Join it to the second
network from its own compose file, keeping its existing one:

```yaml
services:
  cloudflared:
    networks:
      kypost-net:
        ipv4_address: 10.89.0.10
      kymessages-net:
        ipv4_address: 10.91.0.10
networks:
  kypost-net:
    external: true
  kymessages-net:
    external: true
```

Tunnel ingress: `service: http://kymessages:8080`. Then set
`KY_TRUSTED_PROXIES=10.91.0.10/32` and recreate KyMessages.

`external: true` goes on the proxy side only. A proxy that starts before KyMessages
fails with `network kymessages-net declared as external, but could not be found`;
start it again once KyMessages is up.

## Matrix stack behind cloudflared

`docker-compose.matrix.yml` adds Postgres, Synapse, MAS and Element Web. It publishes no
port and its services join the same `kymessages-net` as KyMessages, so the cloudflared
container set up above reaches them by name. Postgres is only on the internal `matrix-db`
network; cloudflared cannot reach it and must never be given a route to it.

Four public hostnames, all https, plus the server name:

| Variable | Example | Routes to |
|---|---|---|
| `KY_MATRIX_HOST` | `https://matrix.example.com` | `http://synapse:8008` |
| `KY_MATRIX_AUTH_HOST` | `https://auth.example.com` | `http://mas:8080` |
| `KY_MATRIX_CHAT_HOST` | `https://chat.example.com` | `http://element:8080` |
| `KY_ADMIN_HOST` | `https://admin.example.com` | `http://kymessages:8080` |
| `KY_MATRIX_SERVER_NAME` | `example.com` | only `/.well-known/matrix/client`, to `http://kymessages:8080` |

The server name is the part after the colon in user IDs (`@alice.q_ky:example.com`). The
four hosts are bare https origins (no path). Set `KY_APP_URL` to the admin host's origin, since that hostname is where KyMessages is served.

Element's container serves on port 8080 (`ELEMENT_WEB_PORT` in the overlay), not 80.

Tunnel `config.yml`, rules in this order, each hostname routed to exactly one target:

```yaml
ingress:
  - hostname: example.com
    path: ^/\.well-known/matrix/client$
    service: http://kymessages:8080
  # Synapse's admin API and the MAS-only API: MAS reaches Synapse internally.
  - hostname: matrix.example.com
    path: ^/_synapse/(admin|mas)/
    service: http_status:404
  - hostname: matrix.example.com
    service: http://synapse:8008
  - hostname: auth.example.com
    service: http://mas:8080
  - hostname: chat.example.com
    service: http://element:8080
  - hostname: admin.example.com
    service: http://kymessages:8080
  - service: http_status:404
```

`/upstream/backchannel-logout/*` on the auth host reaches `mas:8080` through the `auth` route
above: KyIdentity posts there to end a disabled user's sessions, so register that URL on its
client and do not block that path. MAS's admin port 8081 is never routed.

A dashboard-managed tunnel takes the same hostname-to-service pairs as public hostnames. Route
no other MAS port, no Postgres and nothing else on the server-name host. MAS's public listener
is 8080, and the compatibility login is not served on it; its admin listener, 8081, binds only
to `mas-admin` on the internal `matrix-admin` network and is never routed. Element loads its logo
from `KY_ADMIN_HOST`; do not add a proxy-side `img-src` that excludes it.

KyMessages sends HSTS (`includeSubDomains`) only for requests to its own `KY_APP_URL` host,
never on the server-name host, so plain-http services under the apex keep working.

If another site already serves the apex, drop the `example.com` rule and have that site serve
`/.well-known/matrix/client` as a static file instead, with `Content-Type: application/json`
and `Access-Control-Allow-Origin: *`:

```json
{"m.homeserver":{"base_url":"https://matrix.example.com"}}
```

`KY_TRUSTED_PROXIES` stays cloudflared's /32 on `kymessages-net`. Setup order and the
`KY_MATRIX_UID` step are in the README's "Matrix chat" section.

Check after start: `https://<server name>/.well-known/matrix/client` returns
`{"m.homeserver":{"base_url":"https://matrix.example.com"}}`, the chat host loads Element, and
signing in goes through the auth host to KyIdentity.

## nginx

```nginx
map $http_upgrade $connection_upgrade { default upgrade; '' close; }

server {
    listen 443 ssl;
    server_name chat.example.com;
    # ssl_certificate, ssl_certificate_key ...

    location / {
        proxy_pass http://kymessages:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 1h;
    }
}
```

Run
nginx on `kymessages-net` at a pinned address and put that address as a /32, never
the subnet, in `KY_TRUSTED_PROXIES`.

## Verify

Sign in as an operator and open Settings, Network path. It shows your public address
and the direct peer, plus five marks, each Pass or Warn:

- the request came through a trusted proxy (the peer is in `KY_TRUSTED_PROXIES`)
- the proxy reports https (a trusted `X-Forwarded-Proto: https`)
- `KY_APP_URL` is https
- the request host matches `KY_APP_URL`
- `KY_TRUSTED_PROXIES` names single addresses (no subnet, not empty)

All five must be Pass. Your own address, not `10.91.0.10`, must be the one shown.

## Recovering

- `network kymessages-net declared as external, but could not be found`: start
  KyMessages first, then the proxy.
- `network kymessages-net was found but has incorrect label`: the network was made
  by hand, and Compose refuses it. Hand it back:

  ```sh
  docker compose down                                    # in this directory
  docker network inspect kymessages-net \
    -f '{{range .Containers}}{{.Name}} {{end}}'           # what is still attached
  docker network disconnect -f kymessages-net <each>      # or stop those containers
  docker network rm kymessages-net
  docker compose up -d                                    # recreates it, labelled
  ```

  Then start the proxy again. The network holds no state.
- `Pool overlaps with other one on this address space`: the subnet is taken. Change
  `KY_NETWORK_SUBNET` (for example `10.92.0.0/24`), `KY_CONTAINER_IP` and the proxy's
  pinned address together. List what is taken:

  ```sh
  docker network ls -q | xargs docker network inspect \
    -f '{{.Name}} {{range .IPAM.Config}}{{.Subnet}}{{end}}'
  ```
