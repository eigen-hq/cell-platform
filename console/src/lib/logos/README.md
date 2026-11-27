# App logos

Shown in the console next to an app of the same Helm chart, image or name
(`src/lib/logos.ts`). They are the projects' own marks, used to name their
software, and stay under their projects' terms:

| File | Project | Source |
|---|---|---|
| forgejo.svg | Forgejo | codeberg.org/forgejo/governance (CC BY-SA 4.0) |
| nextcloud.svg | Nextcloud | nextcloud.com/brand (trademark of Nextcloud GmbH) |

Changed here: inline `style` attributes became presentation attributes
(`fill`, `stroke`), so the console's CSP (`style-src 'self'`) has nothing to
refuse. The drawing is unchanged.
