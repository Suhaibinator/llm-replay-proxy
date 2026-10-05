"use client";
import { useCallback, useEffect, useState } from "react";

// The viewer's theme choice. "system" follows prefers-color-scheme; the two
// explicit values set data-theme on <html>, which the CSS in globals.css and
// the inline bootstrap script in app/layout.tsx both read.
export type Theme = "system" | "light" | "dark";
const KEY = "replay-lab-theme";

function read(): Theme {
  try {
    const v = localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}
function apply(theme: Theme) {
  const root = document.documentElement;
  if (theme === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", theme);
}

export function useTheme() {
  const [theme, setThemeState] = useState<Theme>("system");
  const [resolved, setResolved] = useState<"light" | "dark">("light");
  useEffect(() => {
    const initial = read();
    setThemeState(initial);
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const update = () =>
      setResolved(
        (document.documentElement.getAttribute("data-theme") as
          "light" | "dark" | null) ?? (media.matches ? "dark" : "light"),
      );
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  const setTheme = useCallback((next: Theme) => {
    setThemeState(next);
    apply(next);
    try {
      if (next === "system") localStorage.removeItem(KEY);
      else localStorage.setItem(KEY, next);
    } catch {}
    const dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
    setResolved(next === "system" ? (dark ? "dark" : "light") : next);
  }, []);
  return { theme, resolved, setTheme };
}
