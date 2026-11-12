{
  buildGoModule,
  go_latest,
  lib,
  source,
  version,
  revision,
}:

(buildGoModule.override { go = go_latest; }) {
  pname = "multica-server";
  inherit version;
  src = source;
  modRoot = "server";
  vendorHash = "sha256-kFDWoj8uRzVyOePCl9tGVarsuYMe8gWHT162AXW7wmU=";

  subPackages = [
    "cmd/server"
    "cmd/migrate"
    "cmd/backfill_task_usage_hourly"
    "cmd/backfill_codex_usage_cache"
  ];
  env.CGO_ENABLED = 0;
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
    "-X main.commit=${revision}"
  ];

  # Database startup and migrations are exercised by the NixOS VM test.
  doCheck = false;

  postInstall = ''
    mv "$out/bin/server" "$out/bin/multica-server"
    mv "$out/bin/migrate" "$out/bin/multica-migrate"
    mv "$out/bin/backfill_task_usage_hourly" "$out/bin/multica-backfill-task-usage-hourly"
    mv "$out/bin/backfill_codex_usage_cache" "$out/bin/multica-backfill-codex-usage-cache"
    install -d "$out/share/multica"
    cp -R migrations "$out/share/multica/migrations"
    install -Dm644 "${source}/LICENSE" "$out/share/licenses/multica/LICENSE"
    install -Dm644 "${source}/NOTICE" "$out/share/licenses/multica/NOTICE"
  '';

  meta = {
    description = "Multica fork backend and migration tools";
    homepage = "https://github.com/Tr0sT/multica";
    mainProgram = "multica-server";
    platforms = lib.platforms.linux;
  };
}
