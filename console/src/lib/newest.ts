// Polls and reloads can overlap: an older answer (or error) may arrive
// after a newer one. newest() hands out a guard per loader; only the newest
// call's outcome counts, an older one comes back as STALE and is dropped,
// whether it succeeded or failed.
export const STALE = Symbol("stale");

export function newest() {
  let seq = 0;
  return async <T>(answer: Promise<T>): Promise<T | typeof STALE> => {
    const mine = ++seq;
    try {
      const v = await answer;
      return mine === seq ? v : STALE;
    } catch (e) {
      if (mine !== seq) return STALE;
      throw e;
    }
  };
}
