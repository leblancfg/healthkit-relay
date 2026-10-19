# Pairing a phone

Pairing gives an iPhone a token of its own, so the admin token stays with you and each phone
can be revoked on its own.

![The Health Relay app after pairing: up to date, records on server, connected](assets/screenshots/home.png){ width="300" align="right" }

## From the setup page

1. Open `https://<server>/setup` (or `http://<server-ip>:8080/setup` on a LAN) on a
   computer and enter the admin token. The sign-in lasts an hour.
2. If more than one address is offered, pick the one the phone will use.
3. Point the iPhone's Camera at the QR code and tap **Open in Health Relay**. On the iPhone
   itself, tap the page's **Open in Health Relay** button instead. The page also shows the
   setup link, with a copy button, to send to the iPhone and open there.
4. The page turns into *iPhone connected*. In the app, allow Health access once: select
   everything, then confirm.

The first sync uploads your whole history. Keep the app open for it; it can take a while
with years of Apple Watch data. After that, iOS wakes the app in the background as new data
arrives (about hourly; Low Power Mode and force-quitting the app pause it). **Sync Now**, or
pulling down on the main screen, syncs at once.

## From the terminal

On the server, `pair` prints the QR code in the terminal:

```sh
docker compose exec relay healthkit-relay pair                    # Docker Compose
sudo healthkit-relay-ctl pair                                     # NixOS
sudo healthkit-relay pair -archive-dir /var/lib/healthkit-relay \
  -public-url https://health.example.com                          # systemd unit
healthkit-relay pair -archive-dir ./data                          # a binary you run yourself
```

The commands (`pair`, `devices`, `revoke`, `token create`, `admin reset`) work on the state
files in the archive directory (`-archive-dir` or `HKR_ARCHIVE_DIR`), while the server runs;
the server rereads the files when they change. Run them as the server's user or as root. They
take the server's flags and `HKR_` variables: `-public-url` sets the address put in the code.

To send a link to your phone instead (by message or email to yourself), make it last longer:

```sh
healthkit-relay pair -archive-dir ./data -valid 2h
```

## Managing devices

The setup page lists paired iPhones with a **Revoke** button. From the terminal:

```sh
healthkit-relay devices -archive-dir ./data
healthkit-relay revoke  -archive-dir ./data 3f9a1c2e
```

Revoking takes effect at once: the app sees it at its next sync and asks to be set up again.
Pairing again from the same iPhone replaces its previous token.

## How setup codes work

- A setup code is 120 random bits, works once and expires after 15 minutes (`-valid` up to a
  day). The server keeps their SHA-256 in `codes.json` in the archive directory, so a code
  survives a restart until it expires.
- The QR code holds `healthrelay://pair?server=<url>&code=<code>`, a link the app registers.
- The app exchanges the code for a 256-bit device token. The server stores only its SHA-256,
  with the device's name and model.
- `/setup` and pairing answer only private networks by default (`-setup-from private`: loopback,
  LAN, Tailscale). `-setup-from off` disables the page; the terminal commands still work.
- Loopback and link-local addresses are never put in a code, since a phone cannot reach them.
  If the page finds no usable address it says so and shows no code: set `-public-url`.

## Manual setup

In the app, **Enter Server Details Instead** takes an address and the admin token; the app
swaps that token for a device token of its own straight away.

An app set up by hand with an earlier version may still upload with the admin token (the
server token of earlier versions). The server accepts that in this release, logs a warning at
most hourly and counts it in `healthkit_relay_admin_token_uploads_total` on `/metrics`. Pair
that iPhone again from `/setup`.

## Leaving

In the app, **Advanced** › **Disconnect** forgets the server on the phone. **Erase Server Data and Start Over**
deletes what this iPhone uploaded, for good, and then uploads it again. To delete it without
a new upload, revoke the device, then erase its data with the admin token:

```sh
curl -X DELETE -H "Authorization: Bearer <admin token>" \
  "http://127.0.0.1:8080/v1/data?confirm=erase&device=<device id>"
```

To delete everything, stop the server and delete the data directory. See [Backups](backups.md) before
you do either.
