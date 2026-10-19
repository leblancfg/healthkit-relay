# NixOS VM test: the module starts the server, which generates the admin token and prints it
# once; signing in to /setup works with it, the state directory holds only its hash, and
# `admin reset` replaces it. scripts/smoke-test.sh pairs, uploads and checks persistence
# across a service restart. Run with `nix flake check` or
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
    import re

    machine.wait_for_unit("healthkit-relay.service")
    machine.wait_for_open_port(8080)

    # First start: the admin token is printed once, with the setup link.
    out = machine.succeed("journalctl -u healthkit-relay -o cat --no-pager")
    tokens = re.findall(r"Admin token: (\S+)", out)
    assert len(tokens) == 1, out
    assert "Sign in at:  http://machine:8080/setup" in out, out
    admin = tokens[0]

    def sign_in(token):
        return machine.succeed(
            f"curl -s -o /dev/null -w '%{{http_code}}' -X POST --data-urlencode token={token} http://127.0.0.1:8080/setup"
        )

    assert sign_in(admin) == "303", "sign-in with the printed token"
    assert sign_in("not-the-token-0123456789") == "401"
    # Only its hash is on disk, private to the service.
    machine.succeed("test \"$(stat -c %a:%U /var/lib/healthkit-relay/admin.json)\" = 600:healthkit-relay")
    machine.succeed("test ! -e /var/lib/healthkit-relay/token")
    machine.fail(f"grep -rqF -e {admin} /var/lib/healthkit-relay")

    machine.succeed(
        "HKR_URL=http://127.0.0.1:8080 HKR_PAIR_CMD=healthkit-relay-pair "
        + "HKR_RESTART_CMD='systemctl restart healthkit-relay' "
        + "bash ${../scripts/smoke-test.sh} >&2"
    )
    # A restart prints no new token.
    out = machine.succeed("journalctl -u healthkit-relay -o cat --no-pager")
    assert len(re.findall(r"Admin token: ", out)) == 1, out

    # Commands run as root on the running service's state; files stay the service's.
    read = machine.succeed("healthkit-relay-ctl token create -name ci").strip()
    machine.succeed(f"test \"$(curl -s -o /dev/null -w '%{{http_code}}' -H 'Authorization: Bearer {read}' http://127.0.0.1:8080/v1/status)\" = 403")
    reset = machine.succeed("healthkit-relay-ctl admin reset")
    new = re.findall(r"Admin token: (\S+)", reset)[0]
    assert sign_in(admin) == "401", "the old admin token after reset"
    assert sign_in(new) == "303", "the new admin token"
    machine.succeed("test -z \"$(find /var/lib/healthkit-relay ! -user healthkit-relay)\"")
    machine.fail(f"grep -rqF -e {new} -e {read} /var/lib/healthkit-relay")
    machine.succeed("systemd-analyze security healthkit-relay.service --threshold=30 --no-pager >&2")
  '';
}
