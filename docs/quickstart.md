# Quick start

From nothing to a syncing iPhone. This uses Docker Compose; every other way to run the server
is in the [setup guides](setup/index.md).

## 1. Run the server

In a new directory, with `192.168.1.10` replaced by the server's address on your network:

```sh
mkdir -p healthkit-relay && cd healthkit-relay
curl -fsSLO https://raw.githubusercontent.com/leblancfg/healthkit-relay/main/docker-compose.yml
mkdir -p data
printf 'HKR_USER=%s\nHKR_PUBLIC_URL=%s\n' "$(id -u):$(id -g)" http://192.168.1.10:8080 > .env
docker compose up -d
curl -s http://127.0.0.1:8080/healthz    # ok
docker compose logs relay                # the admin token and the setup link
```

On first start the server generates the **admin token** and prints it once, with the setup
link, in its log:

```text
    Admin token: Zk3v...
    Sign in at:  http://192.168.1.10:8080/setup
```

Save it in a password manager. It signs in to the setup page, where you pair iPhones and
create read tokens. Phones never see it; each gets a token of its own when paired. Lost it?
`docker compose exec relay healthkit-relay admin reset` prints a new one.
`HKR_PUBLIC_URL` is the address the phone will use to reach the server. More options in
[Docker & Compose](setup/docker.md).

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

1. In the server's directory, run `docker compose exec relay healthkit-relay pair`: it prints
   a QR code. Or open `http://192.168.1.10:8080/setup` on a computer on the same network and
   sign in with the admin token.
2. Point the iPhone's Camera at the QR code and tap **Open in Health Relay**.
3. Health asks once what Health Relay may read: select everything, then confirm.

The first sync uploads your whole history; with years of Apple Watch data that takes a
while, so keep the app open the first time. After that it syncs in the background. More in
[Pairing a phone](pairing.md).

## 4. Next steps

- Sync away from home: [Networking & HTTPS](networking.md). Tailscale gives you HTTPS in a
  few minutes without opening any port.
- Ask questions of your data: [Data model & querying](data.md).
- Dashboards: [Grafana](grafana.md). The Compose file's `metrics` profile adds
  VictoriaMetrics and Grafana next to the server; see
  [Docker & Compose](setup/docker.md#with-victoriametrics-and-grafana).
- Do not skip [Backups](backups.md): this is the only copy of your history outside the phone.
