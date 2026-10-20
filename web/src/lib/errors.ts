export class ApiError extends Error {
	status: number;
	constructor(message: string, status: number) {
		super(message);
		this.status = status;
	}
}

/** Turns a failed response into a sentence for people: the API's {"error"} when it has one. */
export function errorMessage(status: number, body: string): string {
	try {
		const parsed = JSON.parse(body);
		if (parsed && typeof parsed.error === 'string' && parsed.error.trim()) return parsed.error;
	} catch {
		// not JSON; fall through
	}
	if (status === 403) return "Homelab doesn't recognise this device. Sign in again, or ask the owner for access.";
	if (status === 404) return "Homelab couldn't find that.";
	if (status >= 500) return 'Something went wrong inside Homelab. Try again in a moment.';
	return `Something went wrong (error ${status}).`;
}

export const NETWORK_MESSAGE = "Can't reach Homelab. Check your connection and try again.";

export const messageOf = (e: unknown): string => (e instanceof Error ? e.message : String(e));
