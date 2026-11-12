{
  description = "Multica fork packages and native NixOS services";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      pkgsFor = system: import nixpkgs { inherit system; };
      revision = self.rev or self.dirtyRev or "dirty";
      releaseVersion = (builtins.fromJSON (builtins.readFile ./apps/web/package.json)).version;
      version = "${releaseVersion}-fork.${self.shortRev or self.dirtyShortRev or "dirty"}";
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
          common = {
            source = self;
            inherit version;
          };
        in
        rec {
          multica-cli = pkgs.callPackage ./nix/packages/multica-cli.nix (common // { inherit revision; });
          multica-server = pkgs.callPackage ./nix/packages/multica-server.nix (
            common // { inherit revision; }
          );
          multica-web = pkgs.callPackage ./nix/packages/multica-web.nix common;
          default = multica-server;
        }
      );

      nixosModules.multica = import ./nix/modules/multica.nix { inherit self; };

      checks = forAllSystems (
        system:
        {
          inherit (self.packages.${system}) multica-cli multica-server multica-web;
        }
        // nixpkgs.lib.optionalAttrs (system == "x86_64-linux") {
          multica-vm = (pkgsFor system).testers.runNixOSTest (
            import ./nix/tests/multica.nix { inherit self; }
          );
        }
      );

      formatter = forAllSystems (system: (pkgsFor system).nixfmt);
    };
}
