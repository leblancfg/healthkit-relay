# Linux binary + systemd

This guide runs the server directly on a Linux server or VPS (virtual private server),
amd64 or arm64, as a systemd service. systemd is the program that starts and watches services
on most Linux distributions: Debian, Ubuntu, Fedora, Arch and others.

## Install the binary

Each [release](https://github.com/leblancfg/healthkit-relay/releases) has an archive per
platform, `healthkit-relay_X.Y.Z_<os>_<arch>.tar.gz`, and a `checksums.txt` with their
SHA-256 checksums. The Linux archive holds the server, the systemd unit
`packaging/healthkit-relay.service`, the READMEs and the license.

1. Open a root shell:

    ```sh
    sudo -i
    ```

2. Download the archive for your machine, check it and install the server. Set `VERSION` to
   the newest version on the releases page:

    ```sh
    VERSION=0.7.0
    ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
    cd "$(mktemp -d)"
    curl -fLO "https://github.com/leblancfg/healthkit-relay/releases/download/v${VERSION}/healthkit-relay_${VERSION}_linux_${ARCH}.tar.gz"
    curl -fLO "https://github.com/leblancfg/healthkit-relay/releases/download/v${VERSION}/checksums.txt"
    sha256sum --check --ignore-missing checksums.txt
    tar -xzf "healthkit-relay_${VERSION}_linux_${ARCH}.tar.gz"
    install -m 755 healthkit-relay /usr/local/bin/
    healthkit-relay -version
    ```

    `sha256sum` prints `OK` after the archive's name, and the last command prints the
    version, e.g. `0.7.0`.

To build from source instead, with Go: `make server-build` writes `bin/healthkit-relay`, and
the unit is in the repository's `packaging/` folder.

## Run it as a service

Still as root, in the folder where you unpacked the archive:

1. Install the unit and make the settings folder:

    ```sh
    install -m 644 packaging/healthkit-relay.service /etc/systemd/system/
    install -d -m 700 /etc/healthkit-relay
    ```

2. Write the settings file, `/etc/healthkit-relay/env`. Pick the case that fits:

    === "Home network"

        The iPhone syncs when it is on your home Wi-Fi. Replace `192.168.1.10` with the
        machine's IP address (`hostname -I` prints it):

        ```sh
        cat > /etc/healthkit-relay/env <<'EOF'
        HKR_LISTEN=0.0.0.0:8080
        HKR_PUBLIC_URL=http://192.168.1.10:8080
        EOF
        ```

        If the machine has a firewall, open port 8080 to your network, e.g. with ufw:
        `ufw allow from 192.168.1.0/24 to any port 8080 proto tcp`.

    === "Tailscale or reverse proxy (HTTPS)"

        The iPhone syncs from anywhere, over HTTPS. The server listens on `127.0.0.1:8080`,
        and Tailscale or a reverse proxy publishes it: see
        [Networking & HTTPS](../networking.md). Use your HTTPS address:

        ```sh
        echo HKR_PUBLIC_URL=https://health.example.com > /etc/healthkit-relay/env
        ```

3. Start the service, now and at every boot:

    ```sh
    systemctl daemon-reload
    systemctl enable --now healthkit-relay
    systemctl status healthkit-relay
    ```

    `systemctl status` shows `active (running)`.

4. Read the admin token, which the server prints once, on its first start:

    ```sh
    journalctl -u healthkit-relay
    ```

    The journal shows it, with the setup page's address:

    ```text
        Admin token: BBvGW16otZlTAfIc7RbrNmMVd7PXBVU8skBFvHZhO_4
        Setup page:  http://192.168.1.10:8080/setup
    ```

    Save the admin token in a password manager: it signs in to the setup page.

5. Pair your iPhone: open the setup page and sign in with the admin token, or run `pair`
   (see [Commands](#commands)). Then follow [Pairing an iPhone](../pairing.md).

### What the unit sets up

The unit
([`packaging/healthkit-relay.service`](https://github.com/leblancfg/healthkit-relay/blob/main/packaging/healthkit-relay.service))
runs the server as a dynamic user (a user systemd makes for the service), in a sandbox,
with:

- the archive in `/var/lib/healthkit-relay`, mode 0700: `staging.db`, `parquet/`,
  `admin.json`, `devices.json` and `codes.json`. systemd keeps it in
  `/var/lib/private/healthkit-relay`, and `/var/lib/healthkit-relay` is a link to it;
- its settings read from `/etc/healthkit-relay/env`, one `HKR_<FLAG>=value` per line (see
  [Configuration](index.md#configuration)), e.g.:

    ```sh title="/etc/healthkit-relay/env"
    HKR_PUBLIC_URL=https://health.example.com
    # HKR_LISTEN=0.0.0.0:8080                             # iPhones on the home network
    # HKR_FORWARD_URL=http://127.0.0.1:8428/influx/write  # metrics, see Grafana
    ```

- `HKR_LISTEN=127.0.0.1:8080` unless the env file sets another address: the server then
  answers only this machine, for a reverse proxy or `tailscale serve`.

After you edit the env file, run `systemctl restart healthkit-relay`. To change what the env
file cannot, such as the paths, run `systemctl edit healthkit-relay`: it keeps your changes
in a separate file, so an update can replace the unit.

## Commands

`pair` prints a setup code as a QR code in the terminal. Run it as root, with the archive:

```sh
sudo healthkit-relay pair -archive-dir /var/lib/healthkit-relay -public-url http://192.168.1.10:8080
```

The commands read their settings from flags and the environment of your shell, and
`/etc/healthkit-relay/env` belongs to the service. So give `pair` the server address with
`-public-url`, the same as `HKR_PUBLIC_URL` in the env file.

`devices`, `revoke <id>`, `token create -name grafana` and `admin reset` take the same
`-archive-dir`. `admin reset` prints a new admin token, and the old one stops working at
once. Files the commands write keep the owner of the archive, so the service can still read
them.

## Updating

1. Download the new archive and check it as in [Install the binary](#install-the-binary).
2. Install the server and the unit again, then restart:

    ```sh
    install -m 755 healthkit-relay /usr/local/bin/
    install -m 644 packaging/healthkit-relay.service /etc/systemd/system/
    systemctl daemon-reload && systemctl restart healthkit-relay
    ```

From 0.6 and earlier, with an `/etc/healthkit-relay/token`: before the restart, uncomment
the two lines `LoadCredential=` and `Environment=HKR_TOKEN_FILE=` in the new unit. On its
next start, the server imports the token's hash into `admin.json`, and the token keeps
working as the admin token. Once it has started, delete the token file, comment those two
lines again, and restart. If you skip this, the server makes a new admin token and prints it
to the journal.
