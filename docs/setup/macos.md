# macOS (launchd)

A Mac mini or any Mac that stays on makes a fine server. Apple silicon and Intel are both
supported.

## Install

Download `healthkit-relay_<version>_darwin_arm64.tar.gz` (or `_amd64` on Intel) from the
[releases page](https://github.com/leblancfg/healthkit-relay/releases), then:

```sh
tar xzf healthkit-relay_*_darwin_*.tar.gz healthkit-relay
xattr -d com.apple.quarantine healthkit-relay 2>/dev/null   # downloaded binaries are quarantined
sudo install -m 755 healthkit-relay /usr/local/bin/
```

Or with Nix: `nix profile install github:leblancfg/healthkit-relay`.

## Data directory and token

```sh
mkdir -p ~/Library/Application\ Support/healthkit-relay/data
cd ~/Library/Application\ Support/healthkit-relay
head -c 32 /dev/urandom | base64 > token && chmod 600 token
```

## launchd agent

A user agent runs while you are logged in. Replace `YOU` with your user name.

```xml title="~/Library/LaunchAgents/io.github.healthkit-relay.plist"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.github.healthkit-relay</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/healthkit-relay</string>
    <string>-listen</string><string>0.0.0.0:8080</string>
    <string>-archive-dir</string><string>/Users/YOU/Library/Application Support/healthkit-relay/data</string>
    <string>-token-file</string><string>/Users/YOU/Library/Application Support/healthkit-relay/token</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/Users/YOU/Library/Logs/healthkit-relay.log</string>
  <key>StandardErrorPath</key><string>/Users/YOU/Library/Logs/healthkit-relay.log</string>
</dict>
</plist>
```

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/io.github.healthkit-relay.plist
launchctl print gui/$(id -u)/io.github.healthkit-relay | grep state
tail -f ~/Library/Logs/healthkit-relay.log
```

To restart after an update: `launchctl kickstart -k gui/$(id -u)/io.github.healthkit-relay`.

## Tips

- The first time it listens on the network, macOS asks whether to allow incoming
  connections: allow it.
- Stop the Mac from sleeping (System Settings › Energy › *Prevent automatic sleeping when the
  display is off*), or syncs wait until it wakes.
- To run without anyone logged in, put the plist in `/Library/LaunchDaemons/` with a
  `UserName` key, owned by root, and load it with `sudo launchctl bootstrap system ...`.
- Time Machine backs up the data directory with everything else.
