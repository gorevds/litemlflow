import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  integrations: [
    starlight({
      title: 'LiteMLflow',
      description: 'Single-binary MLflow-compatible experiment tracking with first-class LLM trace support.',
      logo: {
        svg: './src/assets/logo.svg',
        replacesTitle: false,
      },
      favicon: '/favicon.svg',
      social: {
        github: 'https://github.com/gorevds/litemlflow',
      },
      customCss: ['./src/styles/custom.css'],
      editLink: {
        baseUrl: 'https://github.com/gorevds/litemlflow/edit/main/docs/',
      },
      lastUpdated: true,
      pagination: true,
      sidebar: [
        {
          label: 'Quick Start',
          items: [
            { label: 'Get started in 30 seconds', slug: 'quickstart' },
          ],
        },
        {
          label: 'Concepts',
          items: [
            { label: 'Vision & design goals', slug: 'vision' },
            { label: 'Architecture', slug: 'architecture' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'Benchmarks', slug: 'bench-v04' },
            { label: 'Governance', slug: 'governance' },
          ],
        },
        {
          label: 'Cookbook',
          items: [
            { label: 'Recipes', slug: 'cookbook' },
          ],
        },
        {
          label: 'Roadmap',
          items: [
            { label: 'Year 1 (delivered)', slug: 'roadmap-y1' },
            { label: 'Year 2', slug: 'roadmap-y2' },
          ],
        },
        {
          label: 'Contributing',
          items: [
            { label: 'Fuzz testing', slug: 'contributing-fuzz' },
            { label: 'Mutation testing', slug: 'contributing-mutation' },
          ],
        },
      ],
      head: [
        {
          tag: 'meta',
          attrs: {
            property: 'og:image',
            content: '/og.png',
          },
        },
        {
          tag: 'meta',
          attrs: {
            property: 'og:description',
            content: '143× faster cold start than MLflow. Single binary. Zero deps.',
          },
        },
      ],
    }),
  ],
  site: 'https://docs.litemlflow.dev',
});
