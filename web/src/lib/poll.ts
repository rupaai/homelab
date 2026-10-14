/**
 * Calls fn now and then again every interval() ms. Pauses while the page is hidden and
 * catches up as soon as it's visible again. Returns a stop function.
 */
export function startPolling(fn: () => Promise<unknown>, interval: () => number): () => void {
	let timer: ReturnType<typeof setTimeout> | undefined;
	let stopped = false;

	async function tick() {
		clearTimeout(timer);
		if (stopped || document.hidden) return;
		try {
			await fn();
		} catch {
			// the caller shows its own errors; keep trying
		}
		if (!stopped && !document.hidden) timer = setTimeout(tick, interval());
	}
	const onVisible = () => {
		if (!document.hidden) tick();
	};
	document.addEventListener('visibilitychange', onVisible);
	tick();
	return () => {
		stopped = true;
		clearTimeout(timer);
		document.removeEventListener('visibilitychange', onVisible);
	};
}
