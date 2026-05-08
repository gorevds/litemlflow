# GitHub social preview image spec

**Dimensions:** 1280×640 px (GitHub's required size for repository social cards)  
**Format:** PNG, sRGB color space  
**Filename:** `marketing/social-preview.png` (upload in GitHub repo Settings → Social preview)

---

## Layout

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                                                                          [dark]  │
│  ●  LiteMLflow                                                                  │
│  (dot mark, 48px,                                                               │
│   #2d6cdf fill)                                                                 │
│                                                                                 │
│  ┌──────────────────────────────────────────────────────────────────────────┐   │
│  │                                                                          │   │
│  │           143× faster cold start than MLflow                            │   │
│  │                                                                          │   │
│  │           (large, white, ~64px, centered horizontally)                  │   │
│  │                                                                          │   │
│  └──────────────────────────────────────────────────────────────────────────┘   │
│                                                                                 │
│  Your experiments, in one file.     Single binary. Zero databases. Apache 2.0.  │
│  (subtext, gray #8b949e, ~24px)                                                 │
│                                                                                 │
│  github.com/litemlflow/litemlflow                    (bottom right, muted)      │
└─────────────────────────────────────────────────────────────────────────────────┘
```

---

## Exact design tokens

| Element | Value |
|---|---|
| Background | `#0d1117` (GitHub dark) |
| Logo dot fill | `#2d6cdf` |
| Logo dot stroke | none |
| Wordmark | `#ffffff`, system-ui 700 weight, 36px |
| Headline | `#ffffff`, system-ui 700 weight, 60px |
| Accent number ("143×") | `#2d6cdf` (or render whole headline in white, number in blue) |
| Subtext | `#8b949e`, 24px, normal weight |
| URL | `#8b949e`, 18px |
| Padding | 64px all sides |

---

## Generation options

**Option A — Satori (programmatic):**  
Use `@vercel/satori` with a JSX template to render the image at build time. Output PNG via `resvg-js`. Add as a `site/src/og-image.tsx` component, called from `astro.config.mjs` via a custom integration.

**Option B — Figma:**  
Create a 1280×640 frame. Use "Roboto" or "Inter" (closest to system-ui in Figma). Export as PNG 1×. File in Figma Community: *search "GitHub Social Preview Template"*.

**Option C — Playwright screenshot:**  
Render an HTML file matching the spec above, take a 1280×640 Playwright screenshot. Reproducible in CI.

---

## Key message hierarchy

1. **"143×"** — the number that stops the scroll. Make it the largest element.
2. **"faster cold start than MLflow"** — context for the number.
3. **"LiteMLflow"** — brand recognition.
4. **URL** — where to go.

Do not clutter with the comparison table. One number. One message.
