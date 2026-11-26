// The backup target in plain terms: the providers the form offers, the
// request it sends, and which check to show while one runs.
import type { components } from "./api/schema";

type Target = components["schemas"]["BackupTarget"];
type Request = components["schemas"]["BackupTargetRequest"];

export const hetznerRegions = [
  { id: "fsn1", city: "Falkenstein" },
  { id: "nbg1", city: "Nuremberg" },
  { id: "hel1", city: "Helsinki" },
] as const;

export const hetznerEndpoint = (region: string) => `https://${region}.your-objectstorage.com`;

export type TargetForm = {
  provider: "hetzner" | "other";
  region: string; // Hetzner's
  endpoint: string;
  otherRegion: string;
  bucket: string;
  accessKey: string;
  secretKey: string;
  prefix: string;
  privateNetwork: boolean; // plain http to a private IP, on purpose (E-68)
};

/** Plain http is a choice the form asks for; the cell refuses it otherwise. */
export const plainHttp = (f: TargetForm) => f.provider === "other" && /^http:\/\//i.test(f.endpoint.trim());

/** The request the form sends; pasted keys often end in a line break. */
export function requestOf(f: TargetForm): Request {
  const hetzner = f.provider === "hetzner";
  return {
    endpoint: hetzner ? hetznerEndpoint(f.region) : f.endpoint.trim(),
    region: (hetzner ? f.region : f.otherRegion.trim()) || undefined,
    bucket: f.bucket.trim(),
    accessKey: f.accessKey.trim(),
    secretKey: f.secretKey.trim(),
    prefix: f.prefix.trim().replace(/^\/+|\/+$/g, "") || undefined,
    ...(plainHttp(f) && f.privateNetwork ? { privateNetwork: true } : {}),
  };
}

const at = (t: Target) => (t.check ? Date.parse(t.check.at) : -Infinity);

/** The target to show: the polled one, unless its check is older than the
 * one the admin just asked for (the operator has not taken it up yet). */
export function shown(polled: Target | null, asked: Target | null): Target | null {
  if (!asked?.check || (polled && at(polled) >= at(asked))) return polled;
  return polled ? { ...polled, check: asked.check } : asked;
}

export const checking = (t: Target | null) => t?.check?.state === "checking";
