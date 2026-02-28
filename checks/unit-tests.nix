# Run compiled Go unit tests (integration test skipped via -short).
#
# Run with: nix build .#checks.x86_64-linux.unit-tests
{ runCommand, nitrous }:

runCommand "nitrous-unit-tests" { } ''
  ${nitrous.unittest}/bin/nitrous.test -test.short -test.v
  touch $out
''
