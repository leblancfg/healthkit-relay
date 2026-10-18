# Networking & HTTPS

The phone has to reach the server. It does not have to reach it all the time: the app keeps
what it has not sent and catches up at the next sync it can complete. Choose one of three
setups.

| | Syncs from | HTTPS | Ports opened | Effort |
|---|---|---|---|---|
| [Tailscale](#tailscale-recommended) | anywhere | yes, automatic | none | low |
| [Reverse proxy (Caddy)](#reverse-proxy-with-caddy) | anywhere | yes, automatic | 443 | medium |
| [LAN only](#lan-only) | home Wi-Fi | no | none | lowest |

!!! danger "Never expose plain HTTP to the internet"
    Tokens and health data would cross the internet in clear text. The app refuses plain
    HTTP except to local addresses, but do not rely on that: if the server is reachable from
    the internet, it must be behind HTTPS.

## Tailscale (recommended)

[Tailscale](https://tailscale.com) puts the server and your iPhone on a private network
that works from anywhere, with no port forwarding, and `tailscale serve` gives the server a
real HTTPS certificate. The free plan is enough.

1. Install Tailscale on the server and on the iPhone, signed in to the same tailnet.
2. In the admin console, enable **MagicDNS** and **HTTPS certificates** (DNS page).
3. Keep the server on `-listen 127.0.0.1:8080` and publish it inside the tailnet:

    ```sh
    sudo tailscale serve --bg --https=443 http://127.0.0.1:8080
    tailscale serve status    # shows https://<machine>.<tailnet>.ts.net
    ```

4. Set `-public-url https://<machine>.<tailnet>.ts.net` (or let the setup page offer it: the
   server finds `tailscale serve` names by itself) and pair.

The phone syncs whenever the Tailscale app is connected. Turn on **VPN On Demand** in the
Tailscale iOS app so background syncs are not missed.

With Docker, run Tailscale on the host as above, or add a Tailscale sidecar container that
shares the network namespace of `healthkit-relay`.

!!! note "Funnel"
    `tailscale funnel` would publish the server on the public internet. You do not need it,
    and it is not recommended: keep it inside your tailnet.

## Reverse proxy with Caddy

If the server already has a public domain name, [Caddy](https://caddyserver.com) gets and
renews a Let's Encrypt certificate by itself.

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

- Keep `-setup-from private` (the default). The setup page and pairing then answer only
  clients on private networks: pair from home, or over a VPN. With a proxy on the same host,
  the server uses the last address in `X-Forwarded-For` to decide.
- Uploads need a valid device token, so the public endpoint is only `/v1/samples`,
  `/v1/status` and `/healthz` in practice. Consider allowing only those paths at the proxy.
- nginx, Traefik and HAProxy work the same way: proxy to `127.0.0.1:8080`, pass
  `X-Forwarded-For` and `X-Forwarded-Host`, and allow request bodies of 32 MB.

## LAN only

Simplest: no certificates, nothing exposed. The phone syncs when it is on your home Wi-Fi.

1. Listen on the LAN: `-listen 0.0.0.0:8080`.
2. Give the server a fixed address (a DHCP reservation in your router).
3. Pair from `http://<server-ip>:8080/setup`.

Notes:

- The app allows plain HTTP to local addresses only (IP addresses, `.local` names). Use the
  IP address or `<hostname>.local`, not a name under a public domain.
- iOS asks once for **Local Network** access; allow it, or the app cannot reach the server
  (Settings › Privacy & Security › Local Network › Health Relay).
- Traffic on the LAN is not encrypted. On a shared or untrusted network, use Tailscale
  instead.
- Background syncs happen only while the phone is home, so data from a trip arrives when
  you are back. Nothing is lost.
