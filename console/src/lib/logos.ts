// App logos: one SVG per package in logos/, found by the app's Helm chart,
// its image or its name. Apps without one get their initials (Logo.svelte).
const files = import.meta.glob("./logos/*.svg", { eager: true, query: "?url", import: "default" }) as Record<string, string>;
const urls = new Map(Object.entries(files).map(([path, url]) => [path.slice("./logos/".length, -".svg".length), url]));

type Source = { helm?: { chart?: string }; image?: { repository?: string } };

/** The names an app's logo may be filed under, most specific first. */
export function logoKeys(app: { name: string; spec?: unknown }): string[] {
  const source = (app.spec as { source?: Source } | undefined)?.source;
  const image = source?.image?.repository?.split("/").pop()?.split(":")[0];
  return [source?.helm?.chart, image, app.name].filter((k): k is string => !!k).map((k) => k.toLowerCase());
}

/** The URL of the app's logo, if the console has one. */
export const logo = (app: { name: string; spec?: unknown }) => logoKeys(app).map((k) => urls.get(k)).find(Boolean);
