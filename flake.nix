{
  description = "healthkit-relay: receive Apple Health samples from your iPhone and archive them";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: {
        default = pkgs.buildGoModule {
          pname = "healthkit-relay";
          version = self.shortRev or "dev";
          src = ./server;
          vendorHash = null; # standard library only
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" "-X main.version=${self.shortRev or "dev"}" ];
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
