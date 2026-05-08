# LiteMLflow docs site

Astro + Starlight. Builds from the `docs/` directory (files are copied into `src/content/docs/`).

## Local development

```bash
cd site
npm install
npm run dev          # http://localhost:4321
```

## Build

```bash
npm run build        # output in site/dist/
npm run preview      # preview the built site
```

## Deploy

The built `site/dist/` directory is a static site — deploy to Netlify, Vercel, Cloudflare Pages, or any static host.

Recommended: Cloudflare Pages with the build command `npm run build` and output directory `dist`.

## Content

Documentation source lives in `../docs/*.md`. To update a page, edit the file in `docs/` and re-copy it here (or set up a CI step to sync).

## Theming

Brand tokens are in `src/styles/custom.css`. Primary accent: `#2d6cdf`.

## TODO before launch

- Generate `/public/og.png` (1200×630) — see the note in that file.
- Record `docs/demo.gif` (90 s, 1280×720) and add to README.
- Register `litemlflow.dev` and set `site:` in `astro.config.mjs`.
- Activate GitHub Sponsors / Polar.sh links.
