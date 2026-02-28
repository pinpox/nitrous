{
  description = "nitrous - Nostr TUI chat client";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "nitrous";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-Ansb+qGFtfH8e0l+N+1UsPvP87QuiULj+8xzkcpeDDQ=";
          doCheck = false;

          outputs = [ "out" "unittest" ];

          postInstall = ''
            go test -c -o nitrous.test .
            install -D nitrous.test $unittest/bin/nitrous.test
            if command -v remove-references-to >/dev/null; then
              remove-references-to -t ${pkgs.go} $unittest/bin/nitrous.test
            fi
          '';
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            gotools
          ];
        };
      }
    ) // {
      # Tests (Linux only), each in a separate derivation.
      checks.x86_64-linux =
        let
          pkgs = nixpkgs.legacyPackages.x86_64-linux;
          nitrous = self.packages.x86_64-linux.default;
        in
        {
          unit-tests = pkgs.callPackage ./checks/unit-tests.nix { inherit nitrous; };
          lint = pkgs.callPackage ./checks/lint.nix { inherit nitrous; };
          integration = pkgs.callPackage ./checks/integration.nix { inherit nitrous; };
        };
    };
}
