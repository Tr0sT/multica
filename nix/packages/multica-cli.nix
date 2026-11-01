{
  buildGoModule,
  go_latest,
  lib,
  source,
  version,
  revision,
}:

# The fork's go.mod can require a newer patch release than the default Go.
(buildGoModule.override { go = go_latest; }) {
  pname = "multica-cli";
  inherit version;
  src = source;
  modRoot = "server";
  subPackages = [ "cmd/multica" ];
  vendorHash = "sha256-kFDWoj8uRzVyOePCl9tGVarsuYMe8gWHT162AXW7wmU=";

  env.CGO_ENABLED = 0;
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
    "-X main.commit=${revision}"
    "-X main.buildCommit=${revision}"
  ];

  # Go tests use PostgreSQL and the repository's CLI test harness.
  doCheck = false;
  doInstallCheck = true;
  installCheckPhase = ''
    runHook preInstallCheck
    "$out/bin/multica" --version | grep -F "commit: ${revision}"
    runHook postInstallCheck
  '';

  postInstall = ''
    install -Dm644 "${source}/LICENSE" "$out/share/licenses/multica/LICENSE"
    install -Dm644 "${source}/NOTICE" "$out/share/licenses/multica/NOTICE"
  '';

  meta = {
    description = "Multica CLI and daemon from the Tr0sT fork";
    homepage = "https://github.com/Tr0sT/multica";
    mainProgram = "multica";
    platforms = lib.platforms.linux;
  };
}
