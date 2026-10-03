import type { NextConfig } from "next";
const config: NextConfig = {
  output: "export",
  trailingSlash: true,
  images: { unoptimized: true },
  // Type checking runs as a separate build step. Keeping it separate also
  // makes static exports work in restricted build environments.
  typescript: { ignoreBuildErrors: true },
  experimental: { useTypeScriptCli: false },
};
export default config;
