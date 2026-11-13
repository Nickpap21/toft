import { GeistSans } from "geist/font/sans";
import { GeistMono } from "geist/font/mono";
import "./globals.css";
import { AuthProvider } from "@/context/AuthContext";
import { ThemeProvider } from "@/components/theme-provider";
import { EventStreamMount, Providers } from "@/components/providers";
import { ThemeScript } from "@/components/theme/theme-script";
import { ThemeAccountSync } from "@/components/theme/theme-account-sync";
import { AppChrome } from "@/components/shell/app-chrome";
import { headers } from "next/headers";

export const metadata = {
  title: "Toft",
  description: "Self-hosted deploys on servers you own",
  robots: {
    index: false,
    follow: false,
    googleBot: { index: false, follow: false },
  },
};

export default async function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  // Set by src/proxy.ts for the Content-Security-Policy; Next puts it on its
  // own inline scripts when it finds it in the request headers.
  const nonce = (await headers()).get("x-nonce") ?? undefined;
  return (
    // The head script sets data-theme, the .dark class and color-scheme on
    // <html> before React hydrates, hence suppressHydrationWarning. The font
    // variables sit on <html> so :root can resolve them.
    <html lang="en" className={`${GeistSans.variable} ${GeistMono.variable}`} suppressHydrationWarning>
      <head>
        <ThemeScript nonce={nonce} />
      </head>
      <body className="antialiased bg-ground text-fg min-h-dvh flex flex-col">
        <ThemeProvider>
          <Providers>
            <AuthProvider>
              <EventStreamMount />
              <ThemeAccountSync />
              <AppChrome>{children}</AppChrome>
            </AuthProvider>
          </Providers>
        </ThemeProvider>
      </body>
    </html>
  );
}
