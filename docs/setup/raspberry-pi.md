# Raspberry Pi & ARM

The server runs well on a Raspberry Pi 4 or 5, or any board with a 64-bit ARM processor. It
is a `linux/arm64` program that uses little processor time and memory between syncs.

You need:

- **A 64-bit operating system**: Raspberry Pi OS (64-bit), Ubuntu, DietPi... To check, run
  `uname -m`: it prints `aarch64`. The server runs on 64-bit ARM (`arm64`) only.
- **An SSD over USB** for the archive, the folder where the server keeps your data. The
  first sync writes years of history, and an SD card wears out and corrupts under that many
  writes.

Then follow one of these guides:

- **Docker**: install Docker with `curl -fsSL https://get.docker.com | sh`, then follow
  [Docker & Compose](docker.md). The image runs on `arm64`, so the same Compose file works.
  Make the server's folder on the SSD, so that its `data` folder is on the SSD too.
- **Binary + systemd**: follow [Linux binary + systemd](linux.md). Its script picks the
  `arm64` archive by itself. The unit's archive is `/var/lib/healthkit-relay`, a link to
  `/var/lib/private/healthkit-relay`: mount the SSD, or a folder on it, at
  `/var/lib/private/healthkit-relay` before the first start.

To check the result, see [Check your setup](index.md#check-your-setup).

## Tips

- The first sync is the heavy part: batches of up to 5,000 records, written to the archive
  and compressed into Parquet every hour. It takes longer than on a desktop computer. After
  that, syncs are small.
- Give the Pi a fixed address with a DHCP reservation in your router, or use Tailscale, so
  the iPhone always finds it.
- Watch the SSD's free space with `df -h`, or in [Grafana](../grafana.md).
