# Quick start

This page takes you, step by step, to an iPhone that syncs its Health data to your own server,
on your home Wi-Fi. It runs the server with Docker Compose; the
[setup guides](setup/index.md) show the other ways. New to the words *server address*,
*setup page* or *admin token*? The [glossary](glossary.md) explains each one with an example.

You need:

- **A computer that stays on**, running Linux or macOS, with Docker. This computer runs the
  server. To install Docker, follow [Docker Engine](https://docs.docker.com/engine/install/)
  on Linux or [Docker Desktop](https://docs.docker.com/desktop/) on a Mac. Check it with
  `docker compose version`: it prints `Docker Compose version v2...`.
- **An iPhone** on the same Wi-Fi network as the server, with the **Health Relay** app from
  the App Store. (To build the app yourself instead: a Mac with Xcode 16 or newer and a paid
  [Apple Developer Program](https://developer.apple.com/programs/) membership, see step 3.)

## 1. Find the server address

The server address is the address your iPhone uses to reach the server. On your home
network it is the IP address of the computer that runs the server, followed by the port
`8080`.

On that computer, run:

```sh
hostname -I               # Linux: the first address, e.g. 192.168.1.10
ipconfig getifaddr en0    # macOS: e.g. 192.168.1.10
```

With `192.168.1.10`, the server address is `http://192.168.1.10:8080`. Use your own address
everywhere this page shows `192.168.1.10`.

!!! tip "Keep the address fixed"
    Your router may give the computer a new address one day. To keep the same one, add a
    *DHCP reservation* for the computer in your router's settings.

## 2. Start the server

1. Make a folder for the server and go into it:

    ```sh
    mkdir -p ~/healthkit-relay && cd ~/healthkit-relay
    ```

2. Download the Compose file, which tells Docker how to run the server:

    ```sh
    curl -fsSLO https://raw.githubusercontent.com/leblancfg/healthkit-relay/main/docker-compose.yml
    ```

    `ls` now shows `docker-compose.yml`.

3. Make the `data` folder, where the server keeps your data (the *archive*), and the `.env`
   file with your settings:

    ```sh
    mkdir -p data
    printf 'HKR_USER=%s\nHKR_PUBLIC_URL=%s\n' "$(id -u):$(id -g)" http://192.168.1.10:8080 > .env
    cat .env
    ```

    `cat .env` shows your user and group IDs and the server address, e.g.:

    ```text
    HKR_USER=1000:1000
    HKR_PUBLIC_URL=http://192.168.1.10:8080
    ```

4. Start the server:

    ```sh
    docker compose up -d
    ```

    Docker downloads the server image and starts it. The last line ends with `Started`. If
    Docker answers `permission denied`, put `sudo` in front of each `docker` command on this
    page.

5. Check that the server answers:

    ```sh
    curl http://127.0.0.1:8080/healthz
    ```

    It prints `ok`.

6. Read the admin token. The admin token is the password of your server: the server makes it
   on its first start and prints it once, in its log.

    ```sh
    docker compose logs relay
    ```

    Look for these two lines:

    ```text
    relay-1  |     Admin token: BBvGW16otZlTAfIc7RbrNmMVd7PXBVU8skBFvHZhO_4
    relay-1  |     Setup page:  http://192.168.1.10:8080/setup
    ```

    Save the admin token in a password manager. You sign in to the setup page with it. Lost
    it? `docker compose exec relay healthkit-relay admin reset` prints a new one.

More settings, updates and the optional dashboards are in
[Docker & Compose](setup/docker.md).

## 3. Install the iPhone app

Install **Health Relay** from the App Store on the iPhone (search for "Health Relay"). It
opens on three steps, starting with **Open your server's setup page**: go on with step 4.

??? note "Build the app yourself instead"

    You build the app with Xcode on your Mac and install it on your iPhone from there. Apple
    requires a paid Apple Developer Program membership to sign apps that read Health data.

    1. Install [XcodeGen](https://github.com/yonaskolb/XcodeGen), which writes the Xcode
       project, e.g. with [Homebrew](https://brew.sh):

        ```sh
        brew install xcodegen
        ```

    2. Download the code and make your settings file:

        ```sh
        git clone https://github.com/leblancfg/healthkit-relay && cd healthkit-relay
        cp ios/Config/Local.xcconfig.example ios/Config/Local.xcconfig
        ```

    3. Open `ios/Config/Local.xcconfig` in a text editor. Set your Team ID (on
       [developer.apple.com](https://developer.apple.com/account), under **Membership**) and a
       bundle identifier of your own:

        ```text
        HKR_BUNDLE_ID = com.yourname.healthkitrelay
        HKR_TEAM_ID = ABCDE12345
        ```

    4. Make the Xcode project and open it:

        ```sh
        make ios-project && open ios/HealthKitRelay.xcodeproj
        ```

    5. Connect your iPhone to the Mac with a cable. In Xcode, choose your iPhone at the top of
       the window, then click **Run** (the triangle). The first time, turn on Developer Mode on
       the iPhone (Settings › Privacy & Security › Developer Mode) and trust the Mac.

    Health Relay opens on the iPhone and shows three steps, starting with
    **Open your server's setup page**. More details:
    [ios/README.md](https://github.com/leblancfg/healthkit-relay/blob/main/ios/README.md).

## 4. Pair the iPhone

Pairing connects the app to your server. The setup page shows a *setup code*: a QR code
that pairs one iPhone, once, within 15 minutes.

1. On a computer on your home network, open the setup page in a browser:
   `http://192.168.1.10:8080/setup`. The page shows **Sign in** and an **Admin token**
   field.
2. Paste the admin token and click **Sign in**. The page shows **Pair your iPhone**, a QR
   code and **Waiting for your iPhone**.
3. On the iPhone, open the Camera and point it at the QR code. Tap **Open in Health Relay**.
4. The app asks whether to connect to your server. Tap **Connect**. If iOS asks to find
   devices on your local network, tap **Allow**.
5. The setup page shows **iPhone paired**. A few seconds later it lists your iPhone under
   **Paired devices**: your iPhone is now a *paired device*.
6. In the app, tap **Continue** under **Allow Health access**. Health asks what Health Relay
   may read: turn on every category, then tap **Allow**. (On iOS 27, tap **Select All**, then
   **All Recorded Data**.)

To pair from the terminal instead, run `docker compose exec relay healthkit-relay pair` in
the server's folder: it prints the QR code. More in [Pairing an iPhone](pairing.md).

## 5. Watch the first sync

The first sync sends your whole Health history. With years of Apple Watch data it takes a
while: keep the app open until it is done.

- The app shows **Syncing**, then **Up to date** and when it last synced, e.g.
  *Synced 2 minutes ago*. Under it, **Records on server** counts the records the server
  holds. The card with your server address shows **Connected as** and your iPhone's name.
- The server log shows a line starting with `batch` for each batch it stores. To follow it,
  run `docker compose logs -f relay`, and press Ctrl+C to stop.

After the first sync, Health Relay syncs in the background several times a day; open the app
to sync right away. iPhone keeps Health data locked while the phone is locked, so background
syncs happen after you unlock it. **Sync Now**, at the bottom of the app, syncs at once.

## 6. Next steps

- Sync away from home: [Networking & HTTPS](networking.md). With Tailscale, the iPhone
  reaches the server over HTTPS from anywhere, and your router stays closed.
- Ask questions of your data: [Data model & querying](data.md), and the
  [Query API](query-api.md) for dashboards, scripts and AI agents.
- Dashboards: [Grafana](grafana.md). The Compose file's `metrics` profile adds
  VictoriaMetrics and Grafana next to the server; see
  [Docker & Compose](setup/docker.md#with-victoriametrics-and-grafana).
- Set up [Backups](backups.md): the archive is the only copy of your history outside the
  iPhone.
