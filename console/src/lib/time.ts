// Times as people say them: "4 min ago", "yesterday 14:02", "12 Nov".

const clock = (d: Date) => d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

/** How long ago a moment was, in the words a person would use. */
export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "never";
  const d = new Date(iso);
  const s = Math.round((now - d.getTime()) / 1000);
  if (s < 45) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 6 * 3600) return `${Math.round(s / 3600)} h ago`;
  const today = new Date(now).toDateString();
  if (d.toDateString() === today) return `today ${clock(d)}`;
  if (d.toDateString() === new Date(now - 86400_000).toDateString()) return `yesterday ${clock(d)}`;
  return d.toLocaleDateString([], { day: "numeric", month: "short" });
}

/** The time of day, with seconds, for log-like lists. */
export const stamp = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
