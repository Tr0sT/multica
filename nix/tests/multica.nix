{ self }:

{
  name = "multica";

  nodes.machine = { pkgs, ... }: {
    imports = [ self.nixosModules.multica ];

    environment.systemPackages = [
      pkgs.curl
      self.packages.${pkgs.stdenv.hostPlatform.system}.multica-cli
    ];

    services.multica = {
      enable = true;
      environmentFile = "/var/lib/multica/multica.env";
    };

    systemd.tmpfiles.rules = [
      "f /var/lib/multica/multica.env 0600 multica multica - JWT_SECRET=vm-test-jwt-secret-change-me"
    ];
    systemd.services.multica-backend.environment.DO_NOT_TRACK = "true";
    virtualisation.memorySize = 2048;
  };

  testScript = ''
    import json

    machine.start()
    machine.wait_for_unit("postgresql.service")
    machine.wait_until_succeeds("test $(systemctl show -p Result --value multica-db-setup.service) = success && test $(systemctl show -p InactiveEnterTimestampMonotonic --value multica-db-setup.service) != 0")
    machine.wait_until_succeeds("test $(systemctl show -p Result --value multica-migrate.service) = success && test $(systemctl show -p InactiveEnterTimestampMonotonic --value multica-migrate.service) != 0")
    machine.wait_for_unit("multica-backend.service")
    machine.wait_for_unit("multica-web.service")
    machine.wait_for_open_port(8080)
    machine.wait_for_open_port(3000)
    health = json.loads(machine.succeed("curl -fsS http://127.0.0.1:8080/health"))
    assert health["commit"] == "${self.rev or self.dirtyRev or "dirty"}", health
    machine.succeed("curl -fsS http://127.0.0.1:8080/readyz")
    machine.succeed("curl -fsS http://127.0.0.1:3000/")
    config = json.loads(machine.succeed("curl -fsS http://127.0.0.1:3000/api/config"))
    assert "allow_signup" in config, config
    assert config["server_version"] == "${self.packages.x86_64-linux.multica-server.version}", config
    machine.succeed("multica --version | grep -F '${self.packages.x86_64-linux.multica-cli.version}'")
    machine.succeed("sudo -u multica psql -d multica -c 'SELECT count(*) FROM agent_marketplace'")
    machine.succeed("test $(stat -c '%U:%G' /var/lib/multica/uploads) = multica:multica")
  '';
}
