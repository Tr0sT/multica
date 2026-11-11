{
  lib,
  stdenv,
  fetchPnpmDeps,
  makeWrapper,
  nodejs_22,
  pnpm_10,
  pnpmConfigHook,
  source,
  version,
  remoteApiUrl ? "http://127.0.0.1:8080",
  nextPublicWsUrl ? "",
  appVersion ? version,
}:

stdenv.mkDerivation (finalAttrs: {
  pname = "multica-web";
  inherit version;

  src = source;

  pnpmDeps = fetchPnpmDeps {
    inherit (finalAttrs) pname version src;
    pnpm = pnpm_10;
    fetcherVersion = 4;
    pnpmWorkspaces = [ "@multica/web..." ];
    hash = "sha256-3xHONMVUWYA/12AhaZQQJBVef3LG+wqj56hF7Z3mFzE=";
  };

  nativeBuildInputs = [
    makeWrapper
    nodejs_22
    pnpm_10
    pnpmConfigHook
  ];

  # shortcut: use system fallback fonts offline; bundle fonts to restore exact typography.
  # Keep Next.js self-contained when Nix builds without Google Fonts access.
  # Fail closed if upstream changes the font declarations or their constructors.
  postPatch = ''
    node <<'JS'
    const fs = require("fs");
    const path = "apps/web/app/layout.tsx";
    let source = fs.readFileSync(path, "utf8");
    const imports = 'import { Inter, Geist_Mono, JetBrains_Mono, Source_Serif_4 } from "next/font/google";';
    if (!source.includes(imports)) {
      throw new Error("Update the offline font patch for upstream's new font imports");
    }
    source = source.replace(imports, "");
    for (const [name, ctor] of [
      ["inter", "Inter"],
      ["geistMono", "Geist_Mono"],
      ["jetbrainsMono", "JetBrains_Mono"],
      ["sourceSerif", "Source_Serif_4"],
    ]) {
      const start = "const " + name + " = " + ctor + "({";
      const at = source.indexOf(start);
      if (at < 0 || source.indexOf(start, at + 1) >= 0) {
        throw new Error("Unexpected or duplicated font declaration: " + name);
      }
      const end = source.indexOf("\n});", at);
      if (end < 0) throw new Error("Unterminated font declaration: " + name);
      source = source.slice(0, at)
        + 'const ' + name + ' = { variable: "" };'
        + source.slice(end + 4);
    }
    fs.writeFileSync(path, source);
    JS
  '';

  pnpmInstallFlags = [ "--filter=@multica/web..." ];

  env = {
    REMOTE_API_URL = remoteApiUrl;
    NEXT_PUBLIC_WS_URL = nextPublicWsUrl;
    NEXT_PUBLIC_APP_VERSION = appVersion;
    STANDALONE = "true";
    NODE_ENV = "production";
    NEXT_TELEMETRY_DISABLED = "1";
  };

  buildPhase = ''
    runHook preBuild
    pnpm --filter @multica/web build
    runHook postBuild
  '';

  installPhase = ''
    runHook preInstall
    install -d "$out/share/multica-web"
    cp -R apps/web/.next/standalone/* "$out/share/multica-web/"
    install -d "$out/share/multica-web/apps/web/.next"
    cp -R apps/web/.next/static "$out/share/multica-web/apps/web/.next/static"
    cp -R apps/web/public "$out/share/multica-web/apps/web/public"
    makeWrapper ${lib.getExe nodejs_22} "$out/bin/multica-web" \
      --chdir "$out/share/multica-web" \
      --set-default NODE_ENV production \
      --set-default HOSTNAME 127.0.0.1 \
      --set-default PORT 3000 \
      --set-default REMOTE_API_URL ${lib.escapeShellArg remoteApiUrl} \
      --set-default NEXT_PUBLIC_WS_URL ${lib.escapeShellArg nextPublicWsUrl} \
      --add-flags "$out/share/multica-web/apps/web/server.js"
    install -Dm644 LICENSE "$out/share/licenses/multica/LICENSE"
    install -Dm644 NOTICE "$out/share/licenses/multica/NOTICE"
    runHook postInstall
  '';

  passthru = {
    inherit
      version
      remoteApiUrl
      nextPublicWsUrl
      appVersion
      ;
  };
  meta = {
    description = "Multica Next.js web frontend";
    homepage = "https://github.com/Tr0sT/multica";
    mainProgram = "multica-web";
    platforms = lib.platforms.linux;
  };
})
