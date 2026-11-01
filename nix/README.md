# Native NixOS deployment

This fork includes the packaging from [multica-nix](https://github.com/Tr0sT/multica-nix).
The CLI/daemon, Go backend, migration tools, and Next.js frontend all build from
the same checkout. No Multica container or upstream release binary is used.
Packages support `x86_64-linux` and `aarch64-linux`; the VM test runs on x86_64.

## NixOS configuration

Add the fork as a flake input and import its module:

```nix
{
  inputs.multica.url = "github:Tr0sT/multica/main";

  outputs = { nixpkgs, multica, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        ./configuration.nix
        multica.nixosModules.multica
        {
          services.multica = {
            enable = true;
            environmentFile = "/var/lib/multica/multica.env";
            frontend.publicUrl = "https://multica.example.com";
            backend.trustedProxies = [ "127.0.0.1/32" ];
          };
        }
      ];
    };
  };
}
```

Keep Multica's nixpkgs input independent if the host uses an older NixOS release:
the fork needs the Go version declared in `server/go.mod`. The module uses the
host's PostgreSQL 17 with pgvector, while application packages use this flake's
locked toolchain.

Provision a strong `JWT_SECRET` and integration credentials in the runtime
`environmentFile`; never put secrets in Nix source or the Nix store. The file
must exist before starting the services. See [the self-hosting guide](../SELF_HOSTING.md)
for authentication and integration settings.

The module starts `multica-db-setup`, `multica-migrate`, `multica-backend`, and
`multica-web`. Data defaults to `/var/lib/multica`; uploads use its `uploads`
directory. Backend and frontend ports default to 8080 and 3000. Put a reverse
proxy or tunnel in front of them. `openFirewall` opens only the frontend;
`backend.openFirewall` controls the backend independently.

## Existing multica-nix configurations

The package names, `nixosModules.multica`, `services.multica` options, service
names, and default data paths are preserved. Keep the existing input name and
change only its URL:

```nix
inputs.multica-nix.url = "github:Tr0sT/multica/main";
```

Then update that input with `nix flake update multica-nix` and build the host
configuration using its normal workflow. Back up PostgreSQL before applying an
application upgrade: starting the new backend runs the fork's migrations.
Importing this module does not itself apply or restart the running system.

## CLI and agent machines

Install `inputs.multica.packages.${pkgs.stdenv.hostPlatform.system}.multica-cli`
in `environment.systemPackages`. Run `multica login` for the desired server,
then use the existing daemon service with
`multica daemon start --foreground --no-auto-update`. Keep
`MULTICA_DAEMON_AUTO_UPDATE=false` in its environment. Agent CLIs and their
credentials remain managed separately on that machine.

Nix owns these binaries: update the flake input instead of running
`multica update`. The package version includes the fork's Git revision, and
`multica --version` reports the full CLI build revision.

## Build and verify

From the repository root:

```bash
nix build .#multica-cli .#multica-server .#multica-web
nix flake check --print-build-logs
```

The VM check starts PostgreSQL, applies migrations, checks backend health and
readiness, loads the frontend, calls the API through the frontend, and verifies
the fork's marketplace schema. It uses an isolated test database and no real
agent accounts. Go unit tests remain in the existing application CI.

`flake.lock` selects the toolchain. When Go dependencies change, refresh
`vendorHash` in both Go derivations. When pnpm dependencies change, refresh
`pnpmDeps.hash` in the web derivation. Set the relevant hash to `lib.fakeHash`,
build that package, then use the actual hash reported by Nix and rebuild.
Nix CI checks source/dependency changes; there is no separate release updater.

## Packaging limitations

- The backend currently binds all interfaces using `PORT`;
  `backend.listenAddress` remains a compatibility option without runtime effect.
  Keep the raw API behind the host firewall or a trusted proxy.
- The offline web build retains multica-nix's system fallback fonts instead of
  downloading Google Fonts. Its typography can differ from the Docker build.
- `web.remoteApiUrl` and `web.nextPublicWsUrl` set build values and wrapper
  runtime defaults. A separate public API origin can be configured with
  `backend.publicUrl`.
