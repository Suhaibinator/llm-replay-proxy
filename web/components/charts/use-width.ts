"use client";
import { useCallback, useState } from "react";

/**
 * Tracks an element's content width so SVG charts can lay out in real pixels
 * (text never scales with the viewport). Width is 0 until first measured.
 */
export function useWidth<T extends HTMLElement>() {
  const [width, setWidth] = useState(0);
  const ref = useCallback((node: T | null) => {
    if (!node) return;
    const measure = () =>
      setWidth((w) => {
        const next = Math.floor(node.getBoundingClientRect().width);
        return next === w ? w : next;
      });
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => observer.disconnect();
  }, []);
  return [ref, width] as const;
}
