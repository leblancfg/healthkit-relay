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
            # tokenFile = "/run/secrets/healthkit-relay";      # default: generated on first start
            # forwardUrl = "http://127.0.0.1:8428/influx/write";
          };
        }
      ];
    };
  };
}
```

Then, after `nixos-rebuild switch`, `sudo healthkit-relay-pair` prints the setup QR code.

## Options

All under `services.healthkit-relay`:

| Option | Default | |
|---|---|---|
| `enable` | `false` | run the server |
| `package` | the flake's package | the `healthkit-relay` package to run |
| `listen` | `"127.0.0.1:8080"` | address to listen on; `"0.0.0.0:8080"` to accept phones on the LAN |
| `publicUrl` | `null` | the URL phones use, put in setup codes. Null: the setup page offers the addresses it finds |
| `tokenFile` | `null` | file holding the server token. Null: a random token is generated on first start in `/var/lib/healthkit-relay/token` |
| `setupFrom` | `"private"` | who may open `/setup` and pair: `"private"`, `"any"` or `"off"` |
| `forwardUrl` | `null` | line-protocol write URL to forward numeric samples to |
| `forwardTokenFile` | `null` | file holding a bearer token for `forwardUrl` |
| `extraFlags` | `[ ]` | extra server flags, e.g. `[ "-measurement" "health" ]` |
| `openFirewall` | `false` | open the `listen` port in the firewall |

The service runs as the system user `healthkit-relay`, with the archive (`staging.db`,
`devices.json`, `parquet/`) in `/var/lib/healthkit-relay`, mode 0700. `tokenFile` and
`forwardTokenFile` are read by systemd as credentials, so they can stay owned by root with
mode 600.

## The token

Never put the token in the Nix configuration: everything there ends up world-readable in
`/nix/store`. Leave `tokenFile` unset to have one generated, or point it at a file outside the
store, created by hand (`install -m 600`) or with
[sops-nix](https://github.com/Mic92/sops-nix) or [agenix](https://github.com/ryantm/agenix).

## Pairing and devices

`healthkit-relay-pair` is installed with the module: it runs `healthkit-relay pair` with the
service's token and settings. Run it as root. For the other commands, pass the token file
yourself:

```sh
sudo healthkit-relay-pair
sudo healthkit-relay devices -token-file /var/lib/healthkit-relay/token   # or your tokenFile
```

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
