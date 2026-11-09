// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightImageZoom from 'starlight-image-zoom';

// desk's documentation, deployed to GitHub Pages by .github/workflows/docs.yml.
// Screenshots in src/assets/shots are made by `npm run shots` (desk's own
// window on a headless GPU, see internal/ui/docshots_test.go), not committed.
export default defineConfig({
	site: 'https://desk.domivega.com',
	integrations: [
		starlight({
			title: 'desk',
			description: 'A Kanban board where every card is a Claude Code session.',
			logo: { src: './src/assets/logo.svg', replacesTitle: false },
			favicon: '/favicon.svg',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/lfdominguez/desk' }],
			editLink: { baseUrl: 'https://github.com/lfdominguez/desk/edit/main/site/' },
			customCss: ['@fontsource/manrope/400.css', '@fontsource/manrope/600.css', '@fontsource/manrope/700.css', '@fontsource/geist-mono/400.css', './src/styles/desk.css'],
			plugins: [starlightImageZoom()],
			lastUpdated: true,
			components: { ThemeProvider: './src/components/ThemeProvider.astro' }, // dark by default, like desk
			sidebar: [
				{
					label: 'Getting started',
					items: ['start/install', 'start/quick-start', 'start/demo', 'start/concepts'],
				},
				{
					label: 'The board',
					items: ['board/overview', 'board/cards', 'board/sidebar', 'board/search', 'board/archive', 'board/palette', 'board/notifications'],
				},
				{
					label: 'Cards',
					items: ['cards/new-card', 'cards/optimize', 'cards/editing', 'cards/links'],
				},
				{
					label: 'Sessions',
					items: [
						'sessions/panel',
						'sessions/full-window',
						'sessions/questions',
						'sessions/shell',
						'sessions/subagents',
						'sessions/canvas',
						'sessions/diffs',
						'sessions/workspace',
						'sessions/terminal',
						'sessions/usage',
					],
				},
				{
					label: 'Drawings',
					items: ['drawings/overview', 'drawings/editor', 'drawings/claude', 'drawings/export'],
				},
				{
					label: 'Organizations',
					items: ['orgs/organizations', 'orgs/settings', 'orgs/accounts', 'orgs/vaults', 'orgs/tickets', 'orgs/ci', 'orgs/alerts', 'orgs/card-tools'],
				},
				{
					label: 'Servers',
					items: [
						'servers/overview',
						'servers/servers-file',
						'servers/panes',
						'servers/infra-claude',
						'servers/palette',
						'servers/cards',
						'servers/host-keys',
						'servers/asbru',
					],
				},
				{
					label: 'Reference',
					items: ['reference/keys', 'reference/cli', 'reference/desk-toml', 'reference/files', 'reference/troubleshooting', 'reference/development'],
				},
			],
		}),
	],
});
