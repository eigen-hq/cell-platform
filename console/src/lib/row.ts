// Where a result about one person shows: in their row, where the button was,
// so nothing moves under the pointer.

/** The row a result about `about` goes to; null puts it above the list, also
 * when that person has no row (yet), as after a failed refresh. */
export function resultRow(about: string | null, rows: readonly { username: string }[]): string | null {
  return about !== null && rows.some((u) => u.username === about) ? about : null;
}
