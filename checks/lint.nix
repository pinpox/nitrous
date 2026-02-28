# Run golangci-lint against the source tree, reusing the vendor
# setup from the main derivation to avoid duplicating vendorHash.
#
# Run with: nix build .#checks.x86_64-linux.lint
{ golangci-lint, nitrous }:

nitrous.overrideAttrs (old: {
  name = "nitrous-lint";
  nativeBuildInputs = old.nativeBuildInputs ++ [ golangci-lint ];
  buildPhase = ''
    HOME=$TMPDIR golangci-lint run
  '';
  doCheck = false;
  outputs = [ "out" ];
  installPhase = ''
    touch $out
  '';
  fixupPhase = ":";
})
