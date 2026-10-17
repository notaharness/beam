// The rate-limit binding counts per minute, in windows aligned to the wall
// clock, and a burst that crosses into the next window starts counting again.
// A test that fills a limit calls freshWindow first: when less than 15 s of
// the current window is left, it waits for the next one to begin.
export async function freshWindow() {
  const left = 60_000 - (Date.now() % 60_000);
  if (left < 15_000) await new Promise((r) => setTimeout(r, left + 100));
}
