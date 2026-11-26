import { defineConfig } from '@rspress/core';

export default defineConfig({
  root: 'docs',
  title: 'sqlglot-go',
  description: 'Parse SQL, inspect schemas, and plan reviewed migrations in Go.',
  lang: 'en',
  route: {
    exclude: ['diagrams/**'],
  },
  markdown: {
    link: { checkDeadLinks: true, checkAnchors: true },
  },
  themeConfig: {
    nav: [
      { text: 'Guide', link: '/guide/getting-started' },
      { text: 'Go API', link: '/guide/go-api' },
      { text: 'Inspection', link: '/guide/inspection' },
      { text: 'Migrations', link: '/guide/migrations' },
    ],
    sidebar: {
      '/': [
        {
          text: 'Using sqlglot-go',
          items: [
            { text: 'Getting started', link: '/guide/getting-started' },
            { text: 'Command line', link: '/guide/cli' },
            { text: 'Web UI', link: '/guide/web-ui' },
            { text: 'Go API', link: '/guide/go-api' },
            { text: 'Connections and inspection', link: '/guide/inspection' },
            { text: 'Migrate and resume work', link: '/guide/migrations' },
          ],
        },
        {
          text: 'Development',
          items: [
            { text: 'Contributing and verification', link: '/guide/contributing' },
            { text: 'Upstream issues', link: '/upstream-issues' },
          ],
        },
      ],
    },
    socialLinks: [
      { icon: 'github', mode: 'link', content: 'https://github.com/flanksource/sqlglot-go' },
    ],
    footer: {
      message: 'Apache 2.0 · Derived from sqlglot (MIT). See LICENSE, LICENSE.sqlglot and NOTICE in the repository.',
    },
  },
});
