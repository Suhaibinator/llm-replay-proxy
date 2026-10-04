import type { Metadata } from "next";
import "./globals.css";
export const metadata: Metadata = {
  title: "Replay Lab",
  description: "Record and replay inference traffic",
};
export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
