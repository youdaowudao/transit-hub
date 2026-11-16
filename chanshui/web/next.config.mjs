/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "export",
  basePath: process.env.NEXT_PUBLIC_TRANSITHUB === "1" ? "/chanshui" : "",
  images: { unoptimized: true },
  ...(process.env.NEXT_PUBLIC_TRANSITHUB === "1" ? { experimental: { cpus: 1 } } : {}),
  distDir: process.env.NEXT_PUBLIC_TRANSITHUB === "1" ? ".next" : "out-build",
};

export default nextConfig;
