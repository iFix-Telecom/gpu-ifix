"use client";

/**
 * "Atualizado há {n}s" stale-data indicator (UI-SPEC §Copywriting) — shows
 * how long ago the last successful React Query refetch landed. Sits next to
 * the page title.
 *
 * Ticks once per second off `dataUpdatedAt` (epoch ms from the query).
 */

import { useEffect, useState } from "react";

export interface StaleIndicatorProps {
  /** `dataUpdatedAt` from the `useQuery` result — epoch ms, 0 if never. */
  updatedAt: number;
}

export function StaleIndicator({ updatedAt }: StaleIndicatorProps) {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);

  if (!updatedAt) return null;

  const seconds = Math.max(0, Math.round((now - updatedAt) / 1000));

  return (
    <span className="text-[12px] font-semibold text-muted-foreground tabular-nums">
      Atualizado há {seconds}s
    </span>
  );
}
