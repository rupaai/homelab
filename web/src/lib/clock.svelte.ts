/** A shared "now" that ticks every 30 seconds so relative times stay fresh. */
export const clock = $state({ now: Date.now() });

if (typeof window !== 'undefined') {
	setInterval(() => (clock.now = Date.now()), 30_000);
}
