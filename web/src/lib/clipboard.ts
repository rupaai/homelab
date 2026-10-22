/** Copies text. Works on plain http too, where navigator.clipboard isn't available. */
export async function copyText(text: string): Promise<boolean> {
	try {
		if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText && globalThis.isSecureContext !== false) {
			await navigator.clipboard.writeText(text);
			return true;
		}
	} catch {
		// Fall through to the older way.
	}
	return copyWithTextarea(text);
}

function copyWithTextarea(text: string): boolean {
	if (typeof document === 'undefined') return false;
	const box = document.createElement('textarea');
	box.value = text;
	box.setAttribute('readonly', '');
	box.setAttribute('aria-hidden', 'true');
	box.style.cssText = 'position:fixed;top:0;left:0;opacity:0;pointer-events:none';
	document.body.appendChild(box);
	try {
		box.focus();
		box.select();
		box.setSelectionRange(0, text.length);
		return document.execCommand('copy');
	} catch {
		return false;
	} finally {
		box.remove();
	}
}

/** True when the browser can open its share sheet. */
export function canShare(): boolean {
	return typeof navigator !== 'undefined' && typeof navigator.share === 'function';
}

/** Opens the share sheet. Closing it without sharing is not an error. */
export async function shareLink(title: string, url: string): Promise<void> {
	try {
		await navigator.share({ title, url });
	} catch (e) {
		if (!(e instanceof DOMException && e.name === 'AbortError')) throw e;
	}
}
