// The platform API, typed from api/openapi.yaml. Every request carries the
// login's token; without one (or when the API refuses it) the browser goes
// to the login, unless the token is brand new (then the API's answer is
// shown instead of logging in again and again).
import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema";
import { justSignedIn, LoginError, signIn, token } from "./auth";

export type Caller = components["schemas"]["Caller"];

const auth: Middleware = {
  async onRequest({ request }) {
    const t = token();
    if (!t) return signIn();
    request.headers.set("Authorization", `Bearer ${t}`);
    return request;
  },
  async onResponse({ response }) {
    if (response.status !== 401) return undefined;
    // A token from a login a moment ago: another login would loop.
    if (justSignedIn()) {
      const body = (await response.clone().json().catch(() => ({}))) as { error?: string };
      throw new LoginError(`The API refused the new login: ${body.error ?? "HTTP 401"}`);
    }
    return signIn();
  },
};

export const api = createClient<paths>({ baseUrl: location.origin });
api.use(auth);

/** Turns an API answer into its data or throws its error message. */
export function must<T>(r: { data?: T; error?: { error: string } | unknown; response: Response }): T {
  if (r.data !== undefined) return r.data;
  const msg = (r.error as { error?: string } | undefined)?.error;
  throw new Error(msg || `The API answered HTTP ${r.response.status}.`);
}
