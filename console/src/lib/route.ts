// The console's routes: the first part of the path picks the view, the
// rest (decoded) is the view's, for example /apps/<name> or
// /organization/<group with spaces>.
export function route(path: string): { section: string; rest?: string } {
  const [, first = "", ...more] = path.split("/");
  let rest: string | undefined = more.join("/") || undefined;
  try {
    rest = rest === undefined ? undefined : decodeURIComponent(rest);
  } catch {
    rest = undefined; // a broken escape opens nothing rather than the wrong thing
  }
  return { section: `/${first}`, rest };
}

/** A path to a view's item, with the item's name escaped. */
export const itemPath = (section: string, name: string) => `${section}/${encodeURIComponent(name)}`;
