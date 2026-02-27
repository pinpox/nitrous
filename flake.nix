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
      # NixOS integration test (Linux only).
      checks.x86_64-linux.integration = import ./test-integration.nix {
        pkgs = nixpkgs.legacyPackages.x86_64-linux;
        inherit self;
      };
    };
}
