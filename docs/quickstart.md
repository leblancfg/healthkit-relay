# Quick start

From nothing to a syncing iPhone. This uses Docker; every other way to run the server is in
the [setup guides](setup/index.md).

## 1. Run the server

```sh
mkdir -p healthkit-relay && cd healthkit-relay
head -c 32 /dev/urandom | base64 > token && chmod 600 token
docker run -d --name healthkit-relay --restart unless-stopped \
  -p 8080:8080 \
  -v "$PWD/data:/data" -v "$PWD/token:/run/secrets/token:ro" \
  -e HKR_LISTEN=0.0.0.0:8080 \
  -e HKR_ARCHIVE_DIR=/data \
  -e HKR_TOKEN_FILE=/run/secrets/token \
  ghcr.io/leblancfg/healthkit-relay:latest
curl -s http://127.0.0.1:8080/healthz    # ok
```

`token` is the **server token**, the admin credential: it opens the setup page and manages
devices. Keep it private. Phones never see it; each gets a token of its own when paired.

## 2. Install the iPhone app

The app is not on the App Store yet, so you build it with Xcode. You need a Mac,
[XcodeGen](https://github.com/yonaskolb/XcodeGen) and a paid Apple Developer Program
membership (HealthKit apps cannot be signed with a free account).

```sh
git clone https://github.com/leblancfg/healthkit-relay && cd healthkit-relay
cp ios/Config/Local.xcconfig.example ios/Config/Local.xcconfig   # set HKR_TEAM_ID and HKR_BUNDLE_ID
make ios-project && open ios/HealthKitRelay.xcodeproj            # then Run on your iPhone
```

Details: [ios/README.md](https://github.com/leblancfg/healthkit-relay/blob/main/ios/README.md).

## 3. Pair

1. On a computer on the same network, open `http://<server-ip>:8080/setup` and enter the
   server token.
2. Point the iPhone's Camera at the QR code and tap **Open in Health Relay**.
3. Health asks once what Health Relay may read: select everything, then confirm.

The first sync uploads your whole history; with years of Apple Watch data that takes a
while, so keep the app open the first time. After that it syncs in the background. More in
[Pairing a phone](pairing.md).

## 4. Next steps

- Sync away from home: [Networking & HTTPS](networking.md). Tailscale gives you HTTPS in a
  few minutes without opening any port.
- Ask questions of your data: [Data model & querying](data.md).
- Dashboards: [Grafana](grafana.md).
- Do not skip [Backups](backups.md): this is the only copy of your history outside the phone.
