import type { OutcomeTone } from "@/lib/traffic";

// Literal class names so Tailwind generates them; colors come from the
// outcome tokens in globals.css.
export const toneBg: Record<OutcomeTone, string> = {
  hit: "bg-hit",
  miss: "bg-miss",
  recorded: "bg-recorded",
  interrupted: "bg-interrupted",
  error: "bg-error",
};

export const toneLabel: Record<OutcomeTone, string> = {
  hit: "Hit",
  miss: "Miss",
  recorded: "Recorded",
  interrupted: "Interrupted",
  error: "Error",
};
