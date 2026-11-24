// CSV that spreadsheets open as plain text: every field quoted, and a field
// that could start a formula (=, +, -, @, their full-width forms, or any
// leading white space) gets a leading apostrophe, since reasons and names
// come from people and agents.

export function csvField(v: string): string {
  const safe = /^[\s=+\-@\uFF1D\uFF0B\uFF0D\uFF20]/.test(v) ? `'${v}` : v;
  return `"${safe.replaceAll('"', '""')}"`;
}

export const csv = (rows: string[][]) => rows.map((r) => r.map(csvField).join(",")).join("\n") + "\n";
