import { describe, expect, it } from 'vitest';
import { errorMessage } from './errors';

describe('errorMessage', () => {
	it('shows the message the API sent', () => {
		expect(errorMessage(400, '{"error":"Use the repo as owner/name."}')).toBe('Use the repo as owner/name.');
	});
	it('ignores an empty error and falls back by status', () => {
		expect(errorMessage(409, '{"error":"  "}')).toBe('Something went wrong (error 409).');
	});
	it('handles non-JSON bodies', () => {
		expect(errorMessage(500, '<html>oops</html>')).toMatch(/inside Homehub/);
		expect(errorMessage(403, '')).toMatch(/Tailscale/);
		expect(errorMessage(404, 'not found')).toMatch(/couldn't find/);
	});
});
