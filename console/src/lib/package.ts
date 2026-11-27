// What the API accepts for a package task (api/v1alpha1/task_types.go):
// the app name pattern of Task.spec.app and the length of Task.spec.input.
export const NAME = /^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$/;
export const MAX_CHARS = 65536;

/** Why a package request would be refused, or "" if it is fine. */
export function packageProblem(name: string, compose: string, taken: Set<string>): string {
  if (name && !NAME.test(name)) return "Starts with a letter; lowercase letters, digits and dashes, up to 40.";
  if (taken.has(name)) return "An app with this name is installed.";
  // Kubernetes counts characters, not bytes.
  if ([...compose].length > MAX_CHARS) return "The file is longer than 65,536 characters.";
  return "";
}
