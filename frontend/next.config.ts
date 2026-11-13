import type { NextConfig } from "next";

const nextConfig: NextConfig = {
    output: "standalone",
    poweredByHeader: false,
    // Settings → Backup became Settings → Backups (D-46).
    async redirects() {
        return [
            { source: "/settings/backup", destination: "/settings/backups", permanent: true },
            // Accounts and operators and Deploy policy moved to Instance (D-68).
            { source: "/settings/accounts", destination: "/instance/accounts", permanent: true },
            { source: "/settings/deploy-policy", destination: "/instance/deploy-policy", permanent: true },
        ];
    },
};

export default nextConfig;
