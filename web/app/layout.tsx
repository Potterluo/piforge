import type { Metadata } from "next";

import { AppProviders } from "@/providers/app-providers";

import "./globals.css";

// No `next/font/google`: it fetches the font at build time, which makes every
// container build depend on fonts.googleapis.com. A sealed build box cannot
// reach it, and a build that fails because a font was unreachable is a bad
// trade for a typeface. The stack below is what the platform-UI fonts resolve
// to anyway, with a CJK fallback that matter for this product.

export const metadata: Metadata = {
  title: "PiForge",
  description: "基于 Pi Agent 的多用户 Web Coding Agent 工作台",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="zh-CN" suppressHydrationWarning className="h-full antialiased">
      <body className="flex min-h-svh flex-col bg-background text-foreground">
        <AppProviders>{children}</AppProviders>
      </body>
    </html>
  );
}
