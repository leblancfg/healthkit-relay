---
description: >-
  Reach your Health Relay server from your iPhone: on your home network only, from anywhere with Tailscale, or behind Caddy with HTTPS.
---

# Networking & HTTPS

Your iPhone reaches the server at the server address, e.g. `http://192.168.1.10:8080` at
home or `https://health.example.ts.net` from anywhere. It needs the server only when it
syncs: the app keeps what it has not sent yet and sends it at the next sync that reaches the
server. Choose one of three setups:

| | Syncs from | HTTPS | Ports opened on your router | Effort |
|---|---|---|---|---|
| [Tailscale](#tailscale-recommended) | anywhere | yes, automatic | none | low |
| [Reverse proxy (Caddy)](#reverse-proxy-with-caddy) | anywhere | yes, automatic | 443 | medium |
| [Home network only](#home-network-only) | your home Wi-Fi | no | none | lowest |

!!! danger "Keep plain HTTP inside your home network"
    Over plain HTTP, tokens and health data cross the network as readable text. The app
    uses plain HTTP only with local addresses. Put any server the internet can reach behind
    HTTPS.

## Tailscale (recommended)

[Tailscale](https://tailscale.com) puts the server and your iPhone on a private network of
your own, a *tailnet*, that works from anywhere. `tailscale serve` gives the server an
HTTPS address with a real certificate, e.g. `https://health.example.ts.net`. The free plan
is enough.

1. Install Tailscale on the computer running the server and on the iPhone, and sign in to
   the same account on both.
2. In the Tailscale admin console, open the **DNS** page and enable **MagicDNS** and
   **HTTPS Certificates**.
3. On the server's computer, publish the server inside your tailnet:

    ```sh
    sudo tailscale serve --bg --https=443 http://127.0.0.1:8080
    tailscale serve status
    ```

    If the first command prints a link to enable Serve, open it and approve. The second
    prints the HTTPS address, e.g. `https://health.example.ts.net`.

4. Set the server address to it and restart the server:

    | Setup | Setting |
    |---|---|
    | Docker Compose | `HKR_PUBLIC_URL=https://health.example.ts.net` in `.env`, then `docker compose up -d` |
    | systemd | `HKR_PUBLIC_URL=https://health.example.ts.net` in `/etc/healthkit-relay/env`, then `systemctl restart healthkit-relay` |
    | NixOS | `publicUrl = "https://health.example.ts.net";`, then `nixos-rebuild switch` |

    The server can stay on `-listen 127.0.0.1:8080`: `tailscale serve` reaches it there.

5. Pair from `https://health.example.ts.net/setup` (see [Pairing an iPhone](pairing.md)).
   An iPhone you already paired with another address needs a new setup code.

The iPhone syncs whenever the Tailscale app is connected. In the Tailscale iOS app, turn on
**VPN On Demand**, so the iPhone stays on your tailnet for background syncs.

With Docker, run Tailscale on the host as above, or add a Tailscale sidecar container that
shares the network namespace of the server's container.

When the server can run the `tailscale` command and no server address is set, the setup
page offers the `ts.net` address under **Server address in the code**.

!!! note "Serve, not Funnel"
    `tailscale funnel` publishes a service on the public internet. Keep the server inside
    your tailnet with `tailscale serve`.

## Reverse proxy with Caddy

A reverse proxy is a web server that receives HTTPS requests and passes them to the server.
If the server's computer has a public domain name, e.g. `health.example.com`,
[Caddy](https://caddyserver.com) gets and renews a Let's Encrypt certificate for it by
itself.

```caddyfile title="/etc/caddy/Caddyfile"
health.example.com {
	reverse_proxy 127.0.0.1:8080
	request_body {
		max_size 32MB
	}
}
```

Then run the server with:

```sh
-listen 127.0.0.1:8080 -public-url https://health.example.com
```

- Keep `-setup-from private`, the default. The setup page and pairing then answer only
  clients on private networks: pair from home, or over a VPN. With a proxy on the same
  computer, the server reads the client's address from the last entry of
  `X-Forwarded-For`.
- The app uses `/v1/pair`, `/v1/samples`, `/v1/status` and `/v1/data`. The setup page is
  under `/setup`, and the [query API](query-api.md) under `/v1/types`, with `/openapi.json`
  and `/docs`. To publish less, allow only the paths you use at the proxy.
- nginx, Traefik and HAProxy work the same way: proxy to `127.0.0.1:8080`, pass
  `X-Forwarded-For` and `X-Forwarded-Host`, and allow request bodies of 32 MB.

## Home network only

The simplest setup: plain HTTP on your home network, with nothing published on the
internet. The iPhone syncs when it is on your home Wi-Fi.

1. Make the server listen on your network: `-listen 0.0.0.0:8080`. The Docker image
   already does.
2. Give the server's computer a fixed address, with a DHCP reservation in your router.
3. Set the server address to it, e.g. `-public-url http://192.168.1.10:8080`.
4. Pair from `http://192.168.1.10:8080/setup`.

Notes:

- The app uses plain HTTP with local addresses: IP addresses and `.local` names. Use the IP
  address, e.g. `http://192.168.1.10:8080`, or `http://<hostname>.local:8080`.
- iOS asks once for **Local Network** access: allow it, so the app can reach the server. To
  change it later: Settings › Privacy & Security › Local Network › Health Relay.
- Anyone on the same network can read traffic over plain HTTP. On a shared network, use
  Tailscale.
- Background syncs happen while the iPhone is at home. Data from a trip reaches the server
  when you are back: the app keeps it until then.
