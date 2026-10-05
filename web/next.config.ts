import type { NextConfig } from "next";
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

// Everything that can change the exported output. Hashing it gives a build ID
// that is stable across rebuilds of the same source (so web/out does not churn
// in git) but changes whenever the output can, which keeps the immutable
// caching of /_next/static/<buildId>/ correct.
const buildInputs = [
  "app",
  "components",
  "lib",
  "package.json",
  "package-lock.json",
  "next.config.ts",
  "postcss.config.mjs",
  "tsconfig.json",
];

function sourceHash(): string {
  const hash = createHash("sha256");
  const files = buildInputs.flatMap((input) =>
    statSync(input).isDirectory()
      ? readdirSync(input, { recursive: true, encoding: "utf8" })
          .map((name) => join(input, name))
          .filter((name) => statSync(name).isFile())
      : [input],
  );
  for (const file of files.sort()) {
    hash.update(file).update("\0").update(readFileSync(file)).update("\0");
  }
  return hash.digest("base64url").slice(0, 21);
}

const config: NextConfig = {
  output: "export",
  trailingSlash: true,
  images: { unoptimized: true },
  generateBuildId: async () => sourceHash(),
  // Type checking runs as a separate build step. Keeping it separate also
  // makes static exports work in restricted build environments.
  typescript: { ignoreBuildErrors: true },
  experimental: { useTypeScriptCli: false },
};
export default config;
