# NixOS

The repository is a flake whose default package is the server. Add it as an input and run it
as a hardened systemd service.

```nix title="flake.nix"
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  inputs.healthkit-relay.url = "github:leblancfg/healthkit-relay";
  inputs.healthkit-relay.inputs.nixpkgs.follows = "nixpkgs";

  outputs = { nixpkgs, healthkit-relay, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      specialArgs = { inherit healthkit-relay; };
      modules = [ ./configuration.nix ./healthkit-relay.nix ];
    };
  };
}
```

```nix title="healthkit-relay.nix"
{ pkgs, healthkit-relay, ... }:
let
  pkg = healthkit-relay.packages.${pkgs.stdenv.hostPlatform.system}.default;
in
{
  environment.systemPackages = [ pkg ];   # for `healthkit-relay pair` and `devices`

  systemd.services.healthkit-relay = {
    description = "Health Relay: Apple Health receiver and archive";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    environment = {
      HKR_LISTEN = "127.0.0.1:8080";
      HKR_ARCHIVE_DIR = "/var/lib/healthkit-relay";
      # HKR_PUBLIC_URL = "https://health.example.com";
      # HKR_FORWARD_URL = "http://127.0.0.1:8428/influx/write";
    };
    serviceConfig = {
      ExecStart = "${pkg}/bin/healthkit-relay -token-file \${CREDENTIALS_DIRECTORY}/token";
      # A file outside the Nix store, e.g. managed by sops-nix or agenix.
      LoadCredential = "token:/var/lib/secrets/healthkit-relay-token";
      DynamicUser = true;
      StateDirectory = "healthkit-relay";
      StateDirectoryMode = "0700";
      Restart = "on-failure";
      NoNewPrivileges = true;
      ProtectSystem = "strict";
      ProtectHome = true;
      PrivateTmp = true;
      PrivateDevices = true;
      RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" ];
    };
  };
}
```

Never put the token in the Nix configuration: everything there ends up world-readable in
`/nix/store`. Create the file by hand (`install -m 600`), or with
[sops-nix](https://github.com/Mic92/sops-nix) or [agenix](https://github.com/ryantm/agenix).

With VictoriaMetrics on the same host, NixOS has a module for it:

```nix
services.victoriametrics = {
  enable = true;
  listenAddress = "127.0.0.1:8428";
  retentionPeriod = "100y";
};
```

To try the server without installing it: `nix run github:leblancfg/healthkit-relay -- -help`.
