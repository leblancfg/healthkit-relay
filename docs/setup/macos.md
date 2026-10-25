# macOS (launchd)

A Mac mini, or any Mac that stays on, can run the server, on Apple silicon or Intel. launchd,
the macOS service manager, starts it when you log in and restarts it if it stops.

Replace `192.168.1.10` below with your Mac's IP address. To find it:

```sh
ipconfig getifaddr en0    # e.g. 192.168.1.10
```

## Install

1. Download `healthkit-relay_<version>_darwin_arm64.tar.gz` from the
   [releases page](https://github.com/leblancfg/healthkit-relay/releases), or `_amd64` on an
   Intel Mac. **About This Mac** tells you which one you have: an Apple M chip is
   Apple silicon (`arm64`).

2. In Terminal, in the folder you downloaded it to, unpack and install the server:

    ```sh
    tar xzf healthkit-relay_*_darwin_*.tar.gz healthkit-relay
    xattr -d com.apple.quarantine healthkit-relay 2>/dev/null   # lets macOS run a downloaded program
    sudo mkdir -p /usr/local/bin
    sudo install -m 755 healthkit-relay /usr/local/bin/
    healthkit-relay -version
    ```

    The last command prints the version, e.g. `0.7.0`.

With Nix, `nix profile install github:leblancfg/healthkit-relay` installs it instead.

## Run it at login

1. Make the archive, the folder where the server keeps your data:

    ```sh
    mkdir -p ~/Library/Application\ Support/healthkit-relay/data
    ```

2. Write the launchd agent, a file that tells launchd how to run the server. This command
   writes it with your home folder and server address filled in:

    ```sh
    mkdir -p ~/Library/LaunchAgents
    cat > ~/Library/LaunchAgents/io.github.healthkit-relay.plist <<EOF
    <?xml version="1.0" encoding="UTF-8"?>
    <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
    <plist version="1.0">
    <dict>
      <key>Label</key><string>io.github.healthkit-relay</string>
      <key>ProgramArguments</key>
      <array>
        <string>/usr/local/bin/healthkit-relay</string>
        <string>-listen</string><string>0.0.0.0:8080</string>
        <string>-public-url</string><string>http://192.168.1.10:8080</string>
        <string>-archive-dir</string><string>$HOME/Library/Application Support/healthkit-relay/data</string>
      </array>
      <key>RunAtLoad</key><true/>
      <key>KeepAlive</key><true/>
      <key>StandardOutPath</key><string>$HOME/Library/Logs/healthkit-relay.log</string>
      <key>StandardErrorPath</key><string>$HOME/Library/Logs/healthkit-relay.log</string>
    </dict>
    </plist>
    EOF
    ```

3. Start it:

    ```sh
    launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/io.github.healthkit-relay.plist
    launchctl print gui/$(id -u)/io.github.healthkit-relay | grep state
    ```

    The second command prints `state = running`. If the macOS firewall is on, macOS asks
    whether `healthkit-relay` may accept incoming connections: click **Allow**.

4. Read the admin token, which the server prints once, on its first start:

    ```sh
    cat ~/Library/Logs/healthkit-relay.log
    ```

    The log shows it, with the setup page's address:

    ```text
        Admin token: BBvGW16otZlTAfIc7RbrNmMVd7PXBVU8skBFvHZhO_4
        Setup page:  http://192.168.1.10:8080/setup
    ```

    Save the admin token in a password manager: it signs in to the setup page.

5. Pair your iPhone: open `http://192.168.1.10:8080/setup` and sign in with the admin token,
   or print the QR code (see below). Then follow [Pairing an iPhone](../pairing.md).

## Commands

The [commands](index.md#commands) run as you, on the same archive. Give them the server
address with `-public-url`:

```sh
healthkit-relay pair -archive-dir ~/Library/Application\ Support/healthkit-relay/data \
  -public-url http://192.168.1.10:8080
healthkit-relay admin reset -archive-dir ~/Library/Application\ Support/healthkit-relay/data   # a new admin token
```

To restart the server after an update:
`launchctl kickstart -k gui/$(id -u)/io.github.healthkit-relay`.

## Tips

- Keep the Mac awake: in System Settings › Energy, turn on *Prevent automatic sleeping when
  the display is off*. While the Mac sleeps, syncs wait until it wakes.
- Give the Mac a fixed address with a DHCP reservation in your router, so the server
  address stays the same.
- A user agent runs while you are logged in. To run the server with nobody logged in, put
  the plist in `/Library/LaunchDaemons/` with a `UserName` key, owned by root, and load it
  with `sudo launchctl bootstrap system ...`.
- Time Machine backs up the archive with the rest of your home folder.
