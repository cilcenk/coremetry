/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'export',
  trailingSlash: true,
  images: { unoptimized: true },
  // In dev, proxy API calls to the Qmetry backend.
  // (rewrites only run in `next dev`, ignored on static export.)
  async rewrites() {
    return [
      { source: '/api/:path*', destination: 'http://localhost:8088/api/:path*' },
    ];
  },
};
export default nextConfig;
