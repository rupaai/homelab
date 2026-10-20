/** The API's answer when there is no valid sign-in (a missing or expired session). */
export const SIGN_IN_MESSAGE = 'Sign in to use Homelab.';

type Listener = () => void;
const listeners = new Set<Listener>();

/** Calls fn whenever the API says the session is gone. Returns a function that stops it. */
export function onSessionLost(fn: Listener): () => void {
	listeners.add(fn);
	return () => listeners.delete(fn);
}

export function sessionLost(): void {
	for (const fn of [...listeners]) fn();
}
