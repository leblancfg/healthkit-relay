---
description: >-
  Run Health Relay on NixOS with the flake's module: a hardened systemd service, with options for the server address, VictoriaMetrics forwarding and read tokens.
---

# NixOS

The repository is a flake with the server package and a NixOS module,
`nixosModules.default`. The module runs the server as a hardened systemd service.

## Install

1. Add the flake as an input of your system's `flake.nix`, import the module and turn on the
   service. This example is for a server on your home network, at `192.168.1.10`:

    ```nix title="flake.nix"
    {
      inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
      inputs.healthkit-relay.url = "github:leblancfg/healthkit-relay";
      inputs.healthkit-relay.inputs.nixpkgs.follows = "nixpkgs";

      outputs = { nixpkgs, healthkit-relay, ... }: {
        nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
          system = "x86_64-linux";
          modules = [
            ./configuration.nix
            healthkit-relay.nixosModules.default
            {
              services.healthkit-relay = {
                enable = true;
                publicUrl = "http://192.168.1.10:8080";
                listen = "0.0.0.0:8080";
                openFirewall = true;
                # forwardUrl = "http://127.0.0.1:8428/influx/write";
              };
            }
          ];
        };
      };
    }
    ```

    With Tailscale or a reverse proxy instead (see [Networking & HTTPS](../networking.md)),
    set `publicUrl` to your HTTPS address, e.g. `"https://health.example.com"`, and leave
    `listen` and `openFirewall` at their defaults.

2. Build and switch to the new configuration:

    ```sh
    sudo nixos-rebuild switch --flake .#myhost
    systemctl status healthkit-relay
    ```

    `systemctl status` shows `active (running)`.

3. Read the admin token, which the service prints once, on its first start:

    ```sh
    sudo journalctl -u healthkit-relay
    ```

    The journal shows it, with the setup page's address:

    ```text
        Admin token: BBvGW16otZlTAfIc7RbrNmMVd7PXBVU8skBFvHZhO_4
        Setup page:  http://192.168.1.10:8080/setup
    ```

    Save the admin token in a password manager: it signs in to the setup page.

4. Pair your iPhone: open the setup page and sign in with the admin token, or print the QR
   code with `sudo healthkit-relay-ctl pair`. Then follow
   [Pairing an iPhone](../pairing.md).

## Options

All under `services.healthkit-relay`:

| Option | Default | |
|---|---|---|
| `enable` | `false` | run the server |
| `package` | the flake's package | the `healthkit-relay` package to run |
| `listen` | `"127.0.0.1:8080"` | address and port to listen on; `"0.0.0.0:8080"` also answers iPhones on your home network |
| `publicUrl` | `null` | the server address, put in setup codes. With `null`, the setup page offers the addresses it finds |
| `tokenFile` | `null` | deprecated: the server token file of earlier versions, imported into `admin.json` at start (see below) |
| `setupFrom` | `"private"` | who may open the setup page and pair: `"private"`, `"any"` or `"off"` |
| `forwardUrl` | `null` | line-protocol write URL to forward numeric samples to |
| `forwardTokenFile` | `null` | file holding a bearer token for `forwardUrl` |
| `extraFlags` | `[ ]` | extra server flags, e.g. `[ "-measurement" "health" ]` |
| `openFirewall` | `false` | open the `listen` port in the firewall |

The service runs as the system user `healthkit-relay`. Its archive is
`/var/lib/healthkit-relay`, mode 0700: `staging.db`, `parquet/`, `admin.json`,
`devices.json` and `codes.json`. systemd reads `forwardTokenFile` as a credential, so the
file can stay owned by root with mode 600.

## The admin token

The service makes the admin token on its first start and prints it once, in the journal
(`sudo journalctl -u healthkit-relay`). `/var/lib/healthkit-relay/admin.json` keeps its
SHA-256 hash. `sudo healthkit-relay-ctl admin reset` prints a new one, and the old one stops
working at once.

From earlier versions: the service imports a `tokenFile`, or the
`/var/lib/healthkit-relay/token` the module made, into `admin.json` at start, and logs a
warning. The token keeps working as the admin token. Once the service has started with this
version, set `tokenFile` to `null` (or remove it) and delete the file.

## Commands

The module installs `healthkit-relay-ctl`, which runs `healthkit-relay` with the service's
settings and archive. Run it as root:

```sh
sudo healthkit-relay-ctl pair                       # a setup code, as a QR code
sudo healthkit-relay-ctl devices                    # paired devices and read tokens
sudo healthkit-relay-ctl revoke 3f9a1c2e
sudo healthkit-relay-ctl token create -name grafana # a read token for the query API
sudo healthkit-relay-ctl admin reset                # a new admin token
```

`healthkit-relay-pair` is short for `healthkit-relay-ctl pair`.

## VictoriaMetrics

To forward numeric samples to VictoriaMetrics on the same machine, turn on its NixOS module
and point the server at it:

```nix
services.victoriametrics = {
  enable = true;
  listenAddress = "127.0.0.1:8428";
  retentionPeriod = "100y";
};
services.healthkit-relay.forwardUrl = "http://127.0.0.1:8428/influx/write";
```

## Trying it

To run the server without installing it: `nix run github:leblancfg/healthkit-relay -- -help`.
`nix flake check` runs the module in a NixOS virtual machine with the end-to-end test.
