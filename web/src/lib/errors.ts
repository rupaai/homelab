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
	if (status === 403) return "Homehub doesn't recognise this device. Open it from a device on your Tailscale network.";
	if (status === 404) return "Homehub couldn't find that.";
	if (status >= 500) return 'Something went wrong inside Homehub. Try again in a moment.';
	return `Something went wrong (error ${status}).`;
}

export const NETWORK_MESSAGE = "Can't reach Homehub. Check your connection and try again.";

export const messageOf = (e: unknown): string => (e instanceof Error ? e.message : String(e));
