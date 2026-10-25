{
  description = "healthkit-relay: receive Apple Health samples from your iPhone and archive them";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = "0.7.0+${self.shortRev or "dirty"}";
    in
    {
      packages = forAll (pkgs: {
        default = pkgs.buildGoModule {
          pname = "healthkit-relay";
          inherit version;
          src = ./server;
          vendorHash = "sha256-TXj30BANiyPHfy/1O5DYL4L9ZL7KvFhi/RGUV9k7LYo="; # update when go.sum changes
          env.CGO_ENABLED = 0; # pure Go SQLite (modernc.org/sqlite): a static binary
          checkFlags = [ "-short" ]; # skip the memory test, which writes a few hundred MB
          nativeCheckInputs = [ pkgs.duckdb ]; # the query API tests run real queries
          ldflags = [ "-s" "-w" "-X main.version=${version}" ];
          postInstall = "mv $out/bin/server $out/bin/healthkit-relay";
          meta = {
            description = "Receiver and archive for Apple Health samples";
            license = pkgs.lib.licenses.mit;
            mainProgram = "healthkit-relay";
          };
        };
      });

      nixosModules.default = import ./nix/module.nix self;

      checks = nixpkgs.lib.genAttrs [ "x86_64-linux" "aarch64-linux" ] (system:
        let pkgs = nixpkgs.legacyPackages.${system}; in {
          package = self.packages.${system}.default;
          nixos = import ./nix/test.nix self { inherit pkgs; };
        });
    };
}
