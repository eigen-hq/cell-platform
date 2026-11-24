// The console's login: OpenID Connect authorization code with PKCE at the
// cell's identity (GET /v0/login says where). Tokens live in sessionStorage,
// so they end with the tab. An expired token means a new login, which the
// identity answers without asking while its own session lasts.
import type { components } from "./schema";

type Login = components["schemas"]["Login"];
type Tokens = { access: string; idToken: string; expires: number; obtained: number };
type Pending = { verifier: string; state: string; back: string };

const TOKENS = "cell.tokens";
const PENDING = "cell.login";

function random(bytes: number): string {
  return b64url(crypto.getRandomValues(new Uint8Array(bytes)));
}

function b64url(data: Uint8Array): string {
  return btoa(String.fromCharCode(...data)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function load<T>(key: string): T | undefined {
  const raw = sessionStorage.getItem(key);
  return raw ? (JSON.parse(raw) as T) : undefined;
}

export class LoginError extends Error {}

async function login(): Promise<Login> {
  const res = await fetch("/v0/login");
  if (!res.ok) throw new LoginError(`The console cannot find its login (HTTP ${res.status}).`);
  return (await res.json()) as Login;
}

/** The current access token, or undefined when a login is needed. */
export function token(): string | undefined {
  const t = load<Tokens>(TOKENS);
  return t && t.expires > Date.now() + 30_000 ? t.access : undefined;
}

/**
 * Whether the token came from a login in the last minute. If the API
 * refuses such a token, a new login would only bring the same answer.
 */
export function justSignedIn(): boolean {
  const t = load<Tokens>(TOKENS);
  return !!t && Date.now() - t.obtained < 60_000;
}

/** Sends the browser to the identity's login. */
export async function signIn(): Promise<never> {
  const l = await login();
  const pending: Pending = { verifier: random(32), state: random(16), back: location.pathname + location.search };
  sessionStorage.setItem(PENDING, JSON.stringify(pending));
  const challenge = b64url(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(pending.verifier))));
  const url = new URL(l.authorizationEndpoint);
  url.search = new URLSearchParams({
    response_type: "code", client_id: l.clientId, redirect_uri: l.redirectUri, scope: l.scope,
    state: pending.state, code_challenge: challenge, code_challenge_method: "S256",
  }).toString();
  location.assign(url);
  return new Promise<never>(() => {});
}

/**
 * Finishes a login when the identity sent the browser back with a code.
 * Returns false when the page was not opened by a login.
 */
export async function finishSignIn(): Promise<boolean> {
  const params = new URLSearchParams(location.search);
  const code = params.get("code"), error = params.get("error");
  if (!code && !error) return false;
  const pending = load<Pending>(PENDING);
  sessionStorage.removeItem(PENDING);
  history.replaceState(null, "", pending?.back ?? "/");
  if (error) throw new LoginError(params.get("error_description") || `The identity refused the login (${error}).`);
  if (!pending || params.get("state") !== pending.state) throw new LoginError("This login was not started here. Sign in again.");
  const l = await login();
  const res = await fetch(l.tokenEndpoint, {
    method: "POST",
    body: new URLSearchParams({
      grant_type: "authorization_code", code: code!, redirect_uri: l.redirectUri, client_id: l.clientId, code_verifier: pending.verifier,
    }),
  });
  const body = (await res.json().catch(() => ({}))) as { access_token?: string; id_token?: string; expires_in?: number; error_description?: string };
  if (!res.ok || !body.access_token) throw new LoginError(body.error_description || `The identity gave no token (HTTP ${res.status}).`);
  const tokens: Tokens = { access: body.access_token, idToken: body.id_token ?? "", expires: Date.now() + (body.expires_in ?? 300) * 1000, obtained: Date.now() };
  sessionStorage.setItem(TOKENS, JSON.stringify(tokens));
  return true;
}

/**
 * Drops the console's token and ends its session at the identity. The
 * token goes even if the identity cannot be reached.
 */
export async function signOut(): Promise<void> {
  const idToken = load<Tokens>(TOKENS)?.idToken;
  sessionStorage.removeItem(TOKENS);
  const url = new URL((await login()).endSessionEndpoint);
  if (idToken) url.searchParams.set("id_token_hint", idToken);
  location.assign(url);
}
