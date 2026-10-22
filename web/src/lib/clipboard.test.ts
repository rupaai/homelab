import { afterEach, describe, expect, it, vi } from 'vitest';
import { canShare, copyText, shareLink } from './clipboard';

function fakeDocument(copyResult: boolean) {
	const box = { value: '', style: { cssText: '' }, setAttribute: vi.fn(), focus: vi.fn(), select: vi.fn(), setSelectionRange: vi.fn(), remove: vi.fn() };
	const doc = {
		createElement: vi.fn(() => box),
		body: { appendChild: vi.fn() },
		execCommand: vi.fn(() => copyResult)
	};
	return { box, doc };
}

describe('copyText', () => {
	afterEach(() => vi.unstubAllGlobals());

	it('uses the clipboard when the browser has it', async () => {
		const writeText = vi.fn().mockResolvedValue(undefined);
		vi.stubGlobal('navigator', { clipboard: { writeText } });
		await expect(copyText('hello')).resolves.toBe(true);
		expect(writeText).toHaveBeenCalledWith('hello');
	});

	it('falls back to a hidden textarea on plain http', async () => {
		const { box, doc } = fakeDocument(true);
		vi.stubGlobal('navigator', {});
		vi.stubGlobal('document', doc);
		await expect(copyText('http://192.168.1.183:7080/invite/x')).resolves.toBe(true);
		expect(box.value).toBe('http://192.168.1.183:7080/invite/x');
		expect(doc.execCommand).toHaveBeenCalledWith('copy');
		expect(box.remove).toHaveBeenCalled();
	});

	it('falls back when the clipboard refuses', async () => {
		const { doc } = fakeDocument(true);
		vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } });
		vi.stubGlobal('document', doc);
		await expect(copyText('x')).resolves.toBe(true);
		expect(doc.execCommand).toHaveBeenCalled();
	});

	it('says so when nothing worked', async () => {
		const { box, doc } = fakeDocument(false);
		vi.stubGlobal('navigator', {});
		vi.stubGlobal('document', doc);
		await expect(copyText('x')).resolves.toBe(false);
		expect(box.remove).toHaveBeenCalled();
	});
});

describe('sharing', () => {
	afterEach(() => vi.unstubAllGlobals());

	it('is offered only when the browser can share', () => {
		vi.stubGlobal('navigator', {});
		expect(canShare()).toBe(false);
		vi.stubGlobal('navigator', { share: vi.fn() });
		expect(canShare()).toBe(true);
	});

	it('ignores a closed share sheet', async () => {
		vi.stubGlobal('navigator', { share: vi.fn().mockRejectedValue(new DOMException('closed', 'AbortError')) });
		await expect(shareLink('Invite', 'http://x')).resolves.toBeUndefined();
	});
});
