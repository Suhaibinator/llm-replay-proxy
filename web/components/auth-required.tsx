"use client";
import { useEffect, useState, type ReactNode } from "react";
import { KeyRound } from "lucide-react";

// The server answers /api requests without a valid console cookie with 401.
// api() calls notifyAuthRequired() on that status and AuthGate swaps the
// whole console for sign-in instructions. Tokens are only ever issued by the
// server binary, so the page can only point at the CLI.
const EVENT = "replay-proxy:auth-required";
let required = false;

export function notifyAuthRequired() {
  if (required) return;
  required = true;
  window.dispatchEvent(new Event(EVENT));
}

export function AuthGate({ children }: { children: ReactNode }) {
  const [blocked, setBlocked] = useState(required);
  useEffect(() => {
    const show = () => setBlocked(true);
    window.addEventListener(EVENT, show);
    if (required) show();
    return () => window.removeEventListener(EVENT, show);
  }, []);
  return blocked ? <AuthRequired /> : <>{children}</>;
}

export function AuthRequired() {
  return (
    <main className="flex min-h-screen items-center justify-center bg-background p-6">
      <section
        role="alert"
        aria-labelledby="auth-required-title"
        className="w-full max-w-xl rounded-xl border border-border bg-card p-8 text-card-foreground shadow-sm"
      >
        <div className="mb-4 flex items-center gap-3">
          <KeyRound className="size-6 text-muted-foreground" aria-hidden />
          <h1 id="auth-required-title" className="text-xl font-semibold">
            Sign-in required
          </h1>
        </div>
        <p className="text-sm text-muted-foreground">
          This replay proxy requires an access token. Tokens can only be issued
          on the server. Run this command there and open the{" "}
          <strong className="text-foreground">Console login link</strong> it
          prints:
        </p>
        <pre className="my-4 overflow-x-auto rounded-md bg-muted px-4 py-3 font-mono text-sm">
          replay-proxy token issue
        </pre>
        <p className="text-sm text-muted-foreground">
          Pass the same <code className="font-mono">-config</code> and{" "}
          <code className="font-mono">-db</code> flags you start the server
          with. If you opened a link before, it has expired or the server&apos;s
          signing key was rotated; issue a new one.
        </p>
      </section>
    </main>
  );
}
