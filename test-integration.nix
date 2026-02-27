# NixOS integration test for nitrous.
#
# Spins up 3 VMs:
#   - relay:   runs strfry nostr relay on port 7777
#   - client1: runs nitrous as "alice"
#   - client2: runs nitrous as "bob"
#
# Test flow:
#   1. Start relay, wait for it to be ready
#   2. Alice creates a channel #testroom
#   3. Bob joins #testroom
#   4. Alice sends a message, verify Bob receives it
#   5. Bob sends a message, verify Alice receives it
#   6. Alice DMs Bob, verify Bob receives it
#   7. Bob DMs Alice, verify Alice receives it
#
# Run with: nix build .#checks.x86_64-linux.integration

{ pkgs, self }:

let
  # Pre-generated test keys (never use these for real!)
  aliceNsec = "nsec1r7jrltlxhzjqfjwmv68ydzq0ucgjdukcq5s7c0awk2z5vypsrpzskwv9z6";
  aliceNpub = "npub1twhxv6ptgag3neu7ex50pp68rrg7qgsk47purmk8wwlxvytmktas94hs5r";

  bobNsec = "nsec1q5pyr8xdmzxjaxs3g7407fezxx9828emyvfkfztuh53stwgndyasrf67ps";
  bobNpub = "npub1cc2ln5qtfn6mgz4ywchurvkr29x8xnqv6aeruyayu4200py08ayqt2d8xk";

  nitrous = self.packages.${pkgs.system}.default;

  # Helper to generate a config file for a client.
  mkConfig = { name, relayUrl }: pkgs.writeText "config.toml" ''
    relays = ["${relayUrl}"]
    max_messages = 500
    private_key_file = "/home/${name}/.config/nitrous/nsec"

    [profile]
    name = "${name}"
    display_name = "${name}"
  '';

  mkNsecFile = nsec: pkgs.writeText "nsec" nsec;

  # Helper script to set up nitrous config directory and start it in tmux.
  mkSetupScript = { name, nsec, relayUrl }: pkgs.writeShellScript "setup-${name}" ''
    set -euo pipefail

    mkdir -p /home/${name}/.config/nitrous
    cp ${mkConfig { inherit name relayUrl; }} /home/${name}/.config/nitrous/config.toml
    chmod 644 /home/${name}/.config/nitrous/config.toml

    cp ${mkNsecFile nsec} /home/${name}/.config/nitrous/nsec
    chmod 600 /home/${name}/.config/nitrous/nsec

    # Fix ownership
    chown -R ${name}:users /home/${name}

    # Start nitrous in a tmux session with debug logging.
    # Run from the user's home dir so debug.log lands there.
    su - ${name} -c 'cd /home/${name} && ${pkgs.tmux}/bin/tmux new-session -d -s nitrous "cd /home/${name} && ${nitrous}/bin/nitrous --config /home/${name}/.config/nitrous/config.toml --debug 2>&1"'
  '';

  # Helper script to send text to nitrous via tmux.
  mkSendScript = name: pkgs.writeShellScript "send-${name}" ''
    su - ${name} -c "${pkgs.tmux}/bin/tmux send-keys -t nitrous -- \"$1\" Enter"
  '';

  # Helper to capture tmux pane content.
  mkCaptureScript = name: pkgs.writeShellScript "capture-${name}" ''
    su - ${name} -c "${pkgs.tmux}/bin/tmux capture-pane -t nitrous -p"
  '';

in pkgs.testers.nixosTest {
  name = "nitrous-integration";

  nodes = {
    relay = { config, pkgs, ... }: {
      services.strfry = {
        enable = true;
        settings = {
          relay = {
            bind = "0.0.0.0";
            port = 7777;
            info = {
              name = "test-relay";
              description = "Integration test relay";
            };
          };
        };
      };

      networking.firewall.allowedTCPPorts = [ 7777 ];
    };

    client1 = { config, pkgs, ... }: {
      environment.systemPackages = [ nitrous pkgs.tmux ];
      users.users.alice = {
        isNormalUser = true;
        home = "/home/alice";
      };
    };

    client2 = { config, pkgs, ... }: {
      environment.systemPackages = [ nitrous pkgs.tmux ];
      users.users.bob = {
        isNormalUser = true;
        home = "/home/bob";
      };
    };
  };

  testScript = let
    setupAlice = mkSetupScript {
      name = "alice";
      nsec = aliceNsec;
      relayUrl = "ws://relay:7777";
    };
    setupBob = mkSetupScript {
      name = "bob";
      nsec = bobNsec;
      relayUrl = "ws://relay:7777";
    };
    sendAlice = mkSendScript "alice";
    sendBob = mkSendScript "bob";
    captureAlice = mkCaptureScript "alice";
    captureBob = mkCaptureScript "bob";
  in ''
    import time

    def wait_for_log(machine, user, pattern, timeout=30):
        """Wait until pattern appears in the user's debug.log."""
        log_path = f"/home/{user}/debug.log"
        for _ in range(timeout * 2):
            status, output = machine.execute(f"cat {log_path} 2>/dev/null")
            if pattern in output:
                return output
            time.sleep(0.5)
        # Dump log for debugging
        _, log = machine.execute(f"cat {log_path} 2>/dev/null")
        # Also check if the process is running
        _, ps = machine.execute("ps aux | grep nitrous")
        raise Exception(f"Timed out waiting for '{pattern}' in {user}'s debug.log.\nLog:\n{log}\nProcesses:\n{ps}")

    def send_keys(machine, script, text):
        """Send text to nitrous via tmux."""
        # Escape double quotes in text for shell
        escaped = text.replace('"', '\\"')
        machine.succeed(f'{script} "{escaped}"')

    def capture_pane(machine, script):
        """Capture tmux pane content."""
        return machine.succeed(f'{script}')

    def get_channel_id(machine, user):
        """Extract channel ID from debug.log after creation."""
        _, log = machine.execute(
            f"grep channelCreatedMsg /home/{user}/debug.log"
        )
        # Format: channelCreatedMsg: id=<hex> name="testroom"
        for line in log.strip().split("\n"):
            if "channelCreatedMsg" in line:
                parts = line.split("id=")[1]
                return parts.split(" ")[0]
        raise Exception(f"Could not find channel ID in log: {log}")

    start_all()

    # ── Step 1: Wait for relay to be ready ──
    with subtest("Relay starts"):
        relay.wait_for_unit("strfry.service")
        relay.wait_for_open_port(7777)

    # ── Step 2: Start nitrous on both clients ──
    with subtest("Start clients"):
        client1.succeed("${setupAlice}")
        client2.succeed("${setupBob}")

        # Wait for both clients to connect and finish NIP-51 list fetch
        wait_for_log(client1, "alice", "nip51ListsFetchedMsg")
        wait_for_log(client2, "bob", "nip51ListsFetchedMsg")
        # Give them a moment to fully initialize
        time.sleep(2)

    # ── Step 3: Alice creates a channel ──
    with subtest("Create channel"):
        send_keys(client1, "${sendAlice}", "/channel create #testroom")
        wait_for_log(client1, "alice", "channelCreatedMsg")
        channel_id = get_channel_id(client1, "alice")
        print(f"Channel ID: {channel_id}")

    # ── Step 4: Bob joins the channel ──
    with subtest("Bob joins channel"):
        send_keys(client2, "${sendBob}", f"/join {channel_id}")
        time.sleep(3)
        # Verify Bob has the channel by checking the pane
        pane = capture_pane(client2, "${captureBob}")
        assert "testroom" in pane, f"Bob should see #testroom in sidebar. Pane:\n{pane}"

    # ── Step 5: Alice sends a message in the channel ──
    with subtest("Alice sends channel message"):
        # Make sure Alice is on the testroom channel
        send_keys(client1, "${sendAlice}", "Hello from Alice!")
        time.sleep(2)

        # Verify Bob receives it
        wait_for_log(client2, "bob", 'content="Hello from Alice!"')

    # ── Step 6: Bob sends a message in the channel ──
    with subtest("Bob sends channel message"):
        send_keys(client2, "${sendBob}", "Hello from Bob!")
        time.sleep(2)

        # Verify Alice receives it
        wait_for_log(client1, "alice", 'content="Hello from Bob!"')

    # ── Step 7: Alice DMs Bob ──
    with subtest("Alice DMs Bob"):
        send_keys(client1, "${sendAlice}", "/dm ${bobNpub}")
        time.sleep(2)
        send_keys(client1, "${sendAlice}", "Secret message from Alice")
        time.sleep(2)

        # Verify Bob receives the DM
        wait_for_log(client2, "bob", 'content="Secret message from Alice"')

    # ── Step 8: Bob DMs Alice ──
    with subtest("Bob DMs Alice"):
        send_keys(client2, "${sendBob}", "/dm ${aliceNpub}")
        time.sleep(2)
        send_keys(client2, "${sendBob}", "Secret reply from Bob")
        time.sleep(2)

        # Verify Alice receives the DM
        wait_for_log(client1, "alice", 'content="Secret reply from Bob"')
  '';
}
