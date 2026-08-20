---
model: opus
effort: medium
---

# Explain why ntnsync exists, on the website

**Date**: 2026-08-20
**Site**: https://fclairamb.github.io/ntnsync/ (Docusaurus 3.10.2, GitHub Pages)

## Problem

The docs explain *what* ntnsync does and never *why* anyone would run it.
`website/docs/intro.md` is a feature list; the landing page is the unmodified
Docusaurus template (hero + a three-item feature grid). A visitor arriving from
GitHub cannot tell in ten seconds why this exists, and the two things that make
the model click — *everything is a page, even databases*, and *properties become
frontmatter verbatim* — are buried in reference tables.

## Content decisions

These are decisions, not suggestions. Rationale is given so they can be argued
with, but an implementer should follow them.

### Three reasons, in this order

1. **Your LLM can actually read it.** The Notion API makes an agent paginate a
   block tree per page. A Markdown tree is grep-able and chunk-able, so the whole
   workspace becomes context.
2. **History with no expiry date.** Notion's page history is capped by plan;
   Git's is not capped at all. Every sync is a commit.
3. **No lock-in, no cliff edge.** If the subscription lapses or the team moves
   tools, the knowledge is already on disk in a format everything reads.

Reason 1 leads because it is the only one that offers a *gain*; the other two are
insurance, and insurance is a weak opening. "Own your knowledge" and "easier to
transition to another solution" were originally separate — they are the same
lock-in argument and are merged into reason 3. Two thin bullets read as padding;
one sharp one lands.

### Two things the copy must not omit

- **Notion already exports Markdown.** A reader who knows this dismisses the page
  unless it is addressed. The differentiators are: it runs *continuously*, file
  paths stay *stable when a page is renamed*, and every version is in *Git* — so
  diffs mean something. Say so explicitly.
- **It is one-way and read-only.** ntnsync never writes to Notion (verified: no
  POST/PATCH/PUT anywhere in `internal/notion`). You get Markdown and history,
  not a workspace you can restore into. State it plainly rather than letting a
  reader infer "backup" and be wrong. It costs one short block and buys trust.

### Promote stable paths

"File paths never change when pages are renamed" is currently one feature bullet
among six. It is what makes the other reasons work — stable paths produce
meaningful diffs, which produce usable history, which is what lets an LLM follow
a document through time. Give it weight.

## Placement

**Constraint that drives this:** `docs/*.md` and `website/docs/*.md` are twins.
They are byte-identical except for a leading `sidebar_position` frontmatter block
on the website copy (`diff docs/markdown-conversion.md
website/docs/markdown-conversion.md` is exactly those four lines), and a prior
commit exists specifically to keep them in sync. A JSX/SVGR import added to a
shared file would render as broken text in the plain copy people read on GitHub.

So **all JSX and diagram usage stays in website-only files.** Only two docs are
website-only: `intro.md` and `deployment.md`.

| Surface | Change |
|---|---|
| `website/src/pages/index.tsx` | Replace the stock three-item feature grid with the three reasons. This is what a visitor from GitHub hits first. |
| `website/docs/intro.md` | Rewrite the opening: one-line thesis, the pipeline diagram, then *everything is a page* and *properties* with their diagrams. Keep the existing Installation / Quick Start sections below, unchanged. |
| `docs/*.md`, `website/docs/{cli-commands,file-architecture,markdown-conversion}.md` | **Do not add diagrams or JSX.** At most add a plain-Markdown cross-link to the intro — and if you do, apply it to *both* twins so they stay in sync. |

## Technical integration

### Rendering the SVGs — use SVGR, not `<img>`

`@svgr/webpack@8.1.0` is present via `@docusaurus/plugin-svgr` (part of the
classic preset), so this works and renders the SVG **inline**:

```tsx
import Pipeline from "@site/static/img/pipeline.svg";
<Pipeline role="img" aria-label="…" />
```

**Do not reference the diagrams as `<img src="/img/pipeline.svg">`.** An `<img>`
loads the SVG as a separate document, page CSS custom properties do not cascade
into it, and the diagram would be stuck in one theme and unreadable in the other.
Inline rendering is what makes theming work.

Inlining also sidesteps `baseUrl`. The site is served from `/ntnsync/`, so any
hand-written absolute asset path is a bug waiting to happen.

**Verify SVGO does not mangle the diagrams.** SVGR runs SVGO by default, which can
strip `<title>` elements and rewrite ids. Check the built output keeps the
`viewBox` and the `var(--…)` fill/stroke values; adjust the diagram source rather
than disabling SVGO if something is dropped.

### Theming — two states, not three

`docusaurus.config.ts` sets `colorMode: { defaultMode: "light",
respectPrefersColorScheme: true }`, and Docusaurus always stamps
`data-theme="light"|"dark"` on `<html>`. There is no un-stamped state to design
around: define tokens under `:root` and `[data-theme="dark"]` only. A
`prefers-color-scheme` media query is unnecessary here and should not be added.

`website/src/css/custom.css` is currently the untouched scaffold — only
`--ifm-color-primary: #2e8555` was changed. Add exactly two semantic tokens to
both blocks:

```css
:root         { --ntn-hosted:#A65A3A; --ntn-owned:#2F6F4E; }
[data-theme="dark"] { --ntn-hosted:#D08A66; --ntn-owned:#6FBF92; }
```

`--ntn-owned` is deliberately close to the site's existing primary green — the
"this is yours, and permanent" colour should read as the brand colour.

Everything else in the diagrams — text, hairlines, muted labels — must use
Infima's existing variables (`--ifm-font-color-base`,
`--ifm-color-emphasis-600`, `--ifm-color-emphasis-300`) so the diagrams follow
the site instead of fighting it. Do not hard-code greys.

### Colour carries meaning

Clay (`--ntn-hosted`) marks what lives in Notion and can go away. Green
(`--ntn-owned`) marks what is on disk and permanent. Apply it consistently across
all three diagrams; it is the through-line, not decoration.

## The three diagrams

All three: `viewBox` set, no fixed `width`/`height`, `max-width:100%`, wrapped in
a container with `overflow-x:auto` so the page body never scrolls sideways.
Each carries `role="img"` and a descriptive `aria-label`.

**1. `pipeline.svg` — how it works.** Four nodes left to right: *Notion*
(hosted; "pages · databases", "block tree") → *ntnsync* (neutral; "blocks →
markdown", "props → frontmatter") → *`*.md`* (owned; "stable paths",
"+ attachments") → *git* (owned; "one commit per sync"). Small caps tags above
each: HOSTED / CONVERT / YOURS / FOREVER. Below, a dashed return arrow pointing
back toward Notion, labelled *nothing is ever written back*, in the hosted
colour — the read-only caveat stated visually, not just in prose.

**2. `everything-is-a-page.svg` — the model.** Two columns, IN NOTION → ON DISK.
Three rows: *Page* → `tech/engineering-wiki.md` (`notion_type: page`);
*Database* → `releases/releases.md` (`notion_type: database`); *Row* (drawn
nested under Database, dashed border) → `releases/client-platform-v0740.md`
(`notion_type: page` + `properties:`). Three different Notion concepts, one shape
on disk.

**3. `properties-frontmatter.svg` — properties.** Left, a Notion property panel;
right, the YAML it becomes. Use the real values from an actual release row:

```yaml
properties:
  Autoprod: false
  Component: "client-platform"
  Issues:
    - "37baa28b-3ffb-8176-9788-c4d8af067810"
  Last edited time: "2026-08-17T11:42:00.000Z"
```

The two points the diagram must make: property names are kept **verbatim**,
spaces and casing included (`Last edited time`, not `last_edited_time`); and
**list-shaped values nest as a YAML sequence** under their own key while
everything else stays flat. Highlight the nested `Issues` entry in the owned
colour.

## Acceptance criteria

- The three reasons appear on the landing page, in the stated order, replacing
  the stock feature grid.
- `website/docs/intro.md` opens with the thesis and pipeline diagram, covers
  *everything is a page* and *properties* with their diagrams, and retains its
  existing Installation and Quick Start content.
- Copy addresses the Notion-export contrast and states the one-way/read-only
  limitation.
- Diagrams are imported via SVGR and render inline. `grep` the built output or
  the source: no `<img src=` pointing at these three SVGs.
- Both themes are legible. Check the light and dark toggle on every diagram — no
  hard-coded grey or black that survives a theme switch.
- No JSX or `import` statements added to any file that has a twin under `docs/`.
  If a shared doc is touched at all, both copies are updated and differ only by
  the `sidebar_position` block.
- `cd website && bun run build` succeeds with no new warnings, and
  `bun run typecheck` passes.
- The page body does not scroll horizontally at a 375px viewport.

## Out of scope

- Restyling the rest of the site or replacing the Docusaurus theme.
- Editing `docs/` twins beyond an optional cross-link.
- A logo, favicon, or social card.
- Translating the diagrams into a Mermaid/other diagram toolchain — hand-authored
  SVG is chosen deliberately so the colour semantics and theming work.
