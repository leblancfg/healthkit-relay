# Raspberry Pi & ARM

The server runs well on a Raspberry Pi 4 or 5, or any 64-bit ARM board: it is a static
`linux/arm64` binary that uses little CPU and memory between syncs.

Requirements:

- a **64-bit** OS (Raspberry Pi OS 64-bit, Ubuntu, DietPi...). Check with `uname -m`: it must
  say `aarch64`. 32-bit (`armv7l`) is not supported.
- **an SSD over USB** for the data directory, not the SD card. The first sync writes years
  of history, and SD cards wear out and corrupt under sustained writes.

Then pick one:

- **Docker:** install it (`curl -fsSL https://get.docker.com | sh`) and follow
  [Docker & Compose](docker.md); the image is multi-arch, so the same Compose file works.
  Keep the Compose directory, and so `data/`, on the SSD.
- **Binary + systemd:** follow [Linux binary + systemd](linux.md); the script there picks
  the `arm64` archive by itself. The unit uses a dynamic user, so the archive really lives in
  `/var/lib/private/healthkit-relay` (`/var/lib/healthkit-relay` is a symlink to it): mount
  the SSD, or a directory on it, there before the first start.

## Tips

- The first sync is the heavy part: tens of thousands of samples per batch, compressed into
  Parquet. Expect it to take longer than on a desktop; afterwards syncs are small.
- Give the Pi a fixed address (a DHCP reservation in your router), or use Tailscale, so the
  phone always finds it.
- Watch the SSD's free space in [Grafana](../grafana.md) or with `df -h`.
