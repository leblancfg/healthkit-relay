# NixOS

The repository is a flake with the server package and a NixOS module,
`nixosModules.default`, that runs it as a hardened systemd service. Add the flake as an input
and import the module:

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
            publicUrl = "https://health.example.com";
            # listen = "0.0.0.0:8080"; openFirewall = true;   # phones on the LAN, no proxy
            # forwardUrl = "http://127.0.0.1:8428/influx/write";
          };
        }
      ];
    };
  };
}
```

Then, after `nixos-rebuild switch`, `journalctl -u healthkit-relay` shows the admin token
and the setup link, printed once on first start; `sudo healthkit-relay-ctl pair` prints the
setup QR code.

## Options

All under `services.healthkit-relay`:

| Option | Default | |
|---|---|---|
| `enable` | `false` | run the server |
| `package` | the flake's package | the `healthkit-relay` package to run |
| `listen` | `"127.0.0.1:8080"` | address to listen on; `"0.0.0.0:8080"` to accept phones on the LAN |
| `publicUrl` | `null` | the URL phones use, put in setup codes. Null: the setup page offers the addresses it finds |
| `tokenFile` | `null` | deprecated: the server token file of earlier versions, imported into `admin.json` at start (see below) |
| `setupFrom` | `"private"` | who may open `/setup` and pair: `"private"`, `"any"` or `"off"` |
| `forwardUrl` | `null` | line-protocol write URL to forward numeric samples to |
| `forwardTokenFile` | `null` | file holding a bearer token for `forwardUrl` |
| `extraFlags` | `[ ]` | extra server flags, e.g. `[ "-measurement" "health" ]` |
| `openFirewall` | `false` | open the `listen` port in the firewall |

The service runs as the system user `healthkit-relay`, with the archive and its state
(`staging.db`, `parquet/`, `admin.json`, `devices.json`, `codes.json`) in
`/var/lib/healthkit-relay`, mode 0700. `forwardTokenFile` is read by systemd as a credential,
so it can stay owned by root with mode 600.

## The admin token

On first start the service generates the admin token and prints it once, with the setup
link, to the journal (`journalctl -u healthkit-relay`). Save it in a password manager: it
signs in to `/setup`. `/var/lib/healthkit-relay/admin.json` holds its SHA-256.
`sudo healthkit-relay-ctl admin reset` replaces it and prints the new one.

From earlier versions: a `tokenFile`, or the `/var/lib/healthkit-relay/token` the module
generated, is imported into `admin.json` at start, so the token keeps working as the admin
token, and a warning is logged. Once the service has started with this version, set
`tokenFile` to null (or remove it) and delete the file.

## Pairing and devices

`healthkit-relay-ctl` is installed with the module: it runs `healthkit-relay` with the
service's settings and archive directory. Run it as root:

```sh
sudo healthkit-relay-ctl pair                       # setup QR code for the app
sudo healthkit-relay-ctl devices                    # paired iPhones and tokens
sudo healthkit-relay-ctl revoke 3f9a1c2e
sudo healthkit-relay-ctl token create -name grafana # a read token for the query API
sudo healthkit-relay-ctl admin reset                # a new admin token
```

`healthkit-relay-pair` is short for `healthkit-relay-ctl pair`.

## VictoriaMetrics

With VictoriaMetrics on the same host, NixOS has a module for it:

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
`nix flake check` runs the module in a NixOS VM with the end-to-end smoke test.
