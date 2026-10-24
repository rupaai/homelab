import { describe, expect, it } from 'vitest';
import { mockApi } from './mock';
import { noteLines, promoteTitle, releaseName } from './format';

describe('mock data shows versions', () => {
	it('gives dev and prod versions, with a commit to fall back on', async () => {
		const apps = await mockApi.apps();
		const movie = apps.find((a) => a.id === 'movie-night')!;
		expect(movie.envs.map((e) => releaseName(e.version))).toEqual(['v0.4.0-dev.2', 'v0.3.0']);
		// An app without versions still shows commits.
		const recipe = apps.find((a) => a.id === 'recipe-box')!;
		expect(recipe.envs.map((e) => releaseName(e.version))).toEqual(['7d2e9aa', '7d2e9aa']);
		expect(apps.find((a) => a.id === 'garden-log')!.versionProblem).toContain('Contents: read and write');
	});

	it('previews the promote with the next prod version, a summary and notes', async () => {
		const p = await mockApi.promotePreview('movie-night');
		expect(p.to?.version).toBe('v0.4.0-dev.2');
		expect(p.nextVersion).toBe('v0.4.0');
		expect(p.summary).toBe('2 new, 1 fix');
		expect(promoteTitle(p.to?.version, p.nextVersion)).toBe('Promote v0.4.0-dev.2 to prod as v0.4.0');
		expect(noteLines(p.notes)).toHaveLength(3);
	});

	it('has no version in the preview when the app has none', async () => {
		const p = await mockApi.promotePreview('recipe-box');
		expect(p.nextVersion).toBe('');
		expect(p.notes).toBe('');
	});
});

describe('mock logs', () => {
	it('counts every level but filters by the chosen ones', async () => {
		const r = await mockApi.searchLogs({ app: 'movie-night', level: ['error'] });
		expect(r.lines.map((l) => l.level)).toEqual(['error']);
		expect(r.counts.info).toBeGreaterThan(0);
	});
	it('gives Homelab lines and pages by id', async () => {
		const h = await mockApi.searchLogs({ app: 'homelab' });
		expect(h.lines.length).toBeGreaterThan(0);
		const first = await mockApi.searchLogs({ limit: 2 });
		const second = await mockApi.searchLogs({ limit: 2, before: Number(first.next) });
		expect(second.lines[0].id).toBeLessThan(first.lines[1].id);
		const tail = await mockApi.searchLogs({ after: first.lines[1].id });
		expect(tail.lines.map((l) => l.id)).toEqual([first.lines[0].id]);
	});
});
