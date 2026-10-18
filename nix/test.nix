# NixOS VM test: the module starts the server, scripts/smoke-test.sh pairs, uploads and
# checks persistence across a service restart. Run with `nix flake check` or
# `nix build .#checks.x86_64-linux.nixos`.
self:
{ pkgs, ... }:
pkgs.testers.runNixOSTest {
  name = "healthkit-relay";
  nodes.machine = {
    imports = [ self.nixosModules.default ];
    services.healthkit-relay = {
      enable = true;
      listen = "0.0.0.0:8080";
      publicUrl = "http://machine:8080";
      openFirewall = true;
    };
    environment.systemPackages = [ pkgs.curl pkgs.jq ];
  };
  testScript = ''
    machine.wait_for_unit("healthkit-relay.service")
    machine.wait_for_open_port(8080)
    machine.succeed(
        "HKR_URL=http://127.0.0.1:8080 HKR_PAIR_CMD=healthkit-relay-pair "
        + "HKR_RESTART_CMD='systemctl restart healthkit-relay' "
        + "bash ${../scripts/smoke-test.sh} >&2"
    )
    # The generated token is private to the service.
    machine.succeed("test \"$(stat -c %a:%U /var/lib/healthkit-relay/token)\" = 600:healthkit-relay")
    machine.succeed("systemd-analyze security healthkit-relay.service --threshold=30 --no-pager >&2")
  '';
}
