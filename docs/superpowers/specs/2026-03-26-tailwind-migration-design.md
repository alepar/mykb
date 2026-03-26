# Tailwind CSS Migration

## Problem

The frontend uses Pico CSS with extensive inline styles. Pico's opinionated defaults limit layout control, leading to workarounds like the `maxWidth: '1400px'` override. Tailwind gives direct utility-class control over every element.

## Approach

Replace Pico CSS with Tailwind v4 (CSS-based config, no tailwind.config.js). Add `@tailwindcss/typography` for markdown rendering. Dark mode via `prefers-color-scheme`.

## Changes

### Dependencies

Remove: `@picocss/pico`

Add: `tailwindcss`, `@tailwindcss/vite`, `@tailwindcss/typography`

### Build Config

`vite.config.ts` — add `@tailwindcss/vite` plugin.

### New File: `src/index.css`

```css
@import "tailwindcss";
@plugin "@tailwindcss/typography";
```

Plus base styles for dark mode background/text colors on `body`.

### `src/main.tsx`

Replace `import '@picocss/pico/css/pico.min.css'` with `import './index.css'`.

### Component Restyling

All 6 component/page files converted from inline `style={{}}` objects and Pico class names to Tailwind utility classes.

**General styling:**
- Dark/light mode via `prefers-color-scheme`
- Neutral gray palette (`gray-900`/`white` backgrounds)
- Nav: horizontal bar with links, dark background
- Tables: `divide-y` borders, compact rows
- Forms: minimal, focus rings
- Markdown: `prose` class from typography plugin
- Container: full-width with horizontal padding, no `max-width` centering

### `src/App.tsx`

Replace `className="container"` with padding-based layout (e.g., `px-6 py-4 max-w-7xl mx-auto`).

## What Does Not Change

- React, React Router, react-markdown, remark-gfm
- Component structure and file organization
- API layer (`api.ts`, `config.ts`)
- Vite dev proxy configuration
- General layout: nav bar + main content area with pages
