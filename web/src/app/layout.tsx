import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  maximumScale: 5,
  userScalable: true,
};

export const metadata: Metadata = {
  title: "Claude Squad - Manage Multiple AI Code Assistants",
  description: "Run multiple AI coding agents in isolated Git worktrees or ordinary-directory shadow workspaces, with explicit three-way Apply and crash recovery.",
  keywords: ["claude", "claude squad", "ai", "code assistant", "terminal", "tmux", "claude code", "codex", "aider", "git worktree", "isolated workspace"],
  authors: [{ name: "yangguol" }, { name: "smtg-ai" }],
  openGraph: {
    title: "Claude Squad",
    description: "Run AI coding agents in isolated Git or ordinary-directory workspaces with explicit, recoverable Apply.",
    url: "https://github.com/yangguol/claude-squad",
    type: "website",
  },
  twitter: {
    card: "summary_large_image",
    title: "Claude Squad",
    description: "Run AI coding agents in isolated Git or ordinary-directory workspaces with explicit, recoverable Apply.",
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body className={`${geistSans.variable} ${geistMono.variable}`}>
        {children}
      </body>
    </html>
  );
}