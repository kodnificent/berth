import type { APIRoute } from 'astro';
import { getCollection } from 'astro:content';

const groups = [
	['Start', (id: string) => !id.includes('/', 'docs/'.length)],
	['Your stack', (id: string) => id.startsWith('docs/stacks/')],
	['Guides', (id: string) => id.startsWith('docs/guides/')],
	['Reference', (id: string) => id.startsWith('docs/reference/')],
	['Background', (id: string) => id.startsWith('docs/background/')],
] as const;

export const GET: APIRoute = async ({ site }) => {
	const pages = (await getCollection('docs'))
		.filter((p) => p.id.startsWith('docs/') && (p.body ?? '').replace(/^#+ .*$/gm, '').trim() !== '')
		.sort((a, b) => (a.data.sidebar?.order ?? 99) - (b.data.sidebar?.order ?? 99) || a.id.localeCompare(b.id));
	const lines = [
		'# Berth',
		'',
		'> Berth gives every git worktree its own environment (ports, names, files and data), so native commands like `docker compose up` and `npm run dev` work in any worktree unchanged. It is a command-line tool and never starts your services.',
		'',
		'Agents working in a worktree should run `berth status --json` first and follow its `next` hints. `berth docs agents` prints the guides built into the binary.',
	];
	for (const [label, belongs] of groups) {
		const items = pages.filter((p) => belongs(p.id));
		if (items.length === 0) continue;
		lines.push('', `## ${label}`, '');
		for (const p of items) {
			const url = new URL(`/${p.id}/`, site).href;
			lines.push(`- [${p.data.title}](${url})${p.data.description ? `: ${p.data.description}` : ''}`);
		}
	}
	return new Response(lines.join('\n') + '\n', { headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
};
