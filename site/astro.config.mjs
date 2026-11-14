// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
	site: 'https://useberth.dev',
	integrations: [
		starlight({
			title: 'berth',
			description: 'Every git worktree gets its own environment: ports, names, files and data.',
			logo: { light: './src/assets/mark.svg', dark: './src/assets/mark-dark.svg' },
			favicon: '/favicon.svg',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/kodnificent/berth' }],
			customCss: ['./src/styles/docs.css'],
			head: [
				{ tag: 'meta', attrs: { property: 'og:image', content: 'https://useberth.dev/og.png' } },
				{ tag: 'meta', attrs: { property: 'og:image:alt', content: 'git worktree copies your code. Berth copies everything else.' } },
				{ tag: 'meta', attrs: { name: 'twitter:image', content: 'https://useberth.dev/og.png' } },
				{ tag: 'link', attrs: { rel: 'preconnect', href: 'https://fonts.googleapis.com' } },
				{ tag: 'link', attrs: { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: true } },
				{
					tag: 'link',
					attrs: {
						rel: 'stylesheet',
						href: 'https://fonts.googleapis.com/css2?family=Big+Shoulders+Display:wght@800&family=Instrument+Sans:wght@400;500;600&family=JetBrains+Mono:wght@400;500&display=swap',
					},
				},
			],
			sidebar: [
				{
					label: 'Start',
					items: [
						{ label: 'Quickstart', slug: 'docs/quickstart' },
						{ label: 'How berth works', slug: 'docs/how-it-works' },
					],
				},
				{ label: 'Your stack', items: [{ autogenerate: { directory: 'docs/stacks' } }] },
				{ label: 'Guides', items: [{ autogenerate: { directory: 'docs/guides' } }] },
				{ label: 'Reference', items: [{ autogenerate: { directory: 'docs/reference' } }] },
				{ label: 'Background', items: [{ autogenerate: { directory: 'docs/background' } }] },
			],
		}),
	],
});
