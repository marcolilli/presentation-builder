# Presentation Builder

Write a presentation in one Obsidian-flavored Markdown file, then generate two standalone HTML files:

- `your-talk.html`
- `your-talk.notes.html`

The generated files inline their CSS and JavaScript. Referenced local assets are copied into the generated presentation folder automatically.

## What This Repo Contains

- `build.js`: converts one Markdown file into a slide deck and a notes view
- `serve.js`: local server for opening, rebuilding, and live-reloading generated decks
- `presentations/`: generated output folders

This repo does not currently store the source Markdown files for the sample presentations. Those are expected to live wherever you author them.

## Requirements

- Node.js
- no install step and no external npm dependencies

## Quick Start

Start the local server:

```bash
npm start
```

Open `http://127.0.0.1:4747/` in your browser, then enter the absolute path to your Markdown file in the build form.

## Output

Each build writes to:

```text
presentations/<presentation-name>/
```

That folder contains:

- `<presentation-name>.html`
- `<presentation-name>.notes.html`
- `presentation.json`
- `assets/` for copied local images and videos when needed

`presentation.json` stores the original absolute source path so the local server can rebuild and watch the Markdown file later.

## Local Server

Do not open the generated files with `file://` if you want slide syncing, notes syncing, or live reload.
The presentation window and notes window communicate through `BroadcastChannel`, which needs the same origin.

The home page at `http://127.0.0.1:4747/` shows:

- generated presentations
- links to open slides and notes
- a form to build a presentation from a Markdown path
- a rebuild button for presentations with saved source metadata
- a live reload toggle per presentation

When live reload is enabled, the server watches the original Markdown file, rebuilds on change, and reloads open slide and notes windows.

You can also open generated files directly through the server, for example:

```text
http://127.0.0.1:4747/presentations/my-talk/my-talk.html
http://127.0.0.1:4747/presentations/my-talk/my-talk.notes.html
```

## Manual Build

You can also build a deck directly from the command line:

```bash
npm run build -- /absolute/path/to/your-talk.md
```

## Authoring Format

Slides are separated by `---`.

```md
# Intro

One sentence here.

---

# Second slide

- Point one
- Point two
```

The builder always inserts a generated cover slide as slide 1.
Its title comes from the Markdown filename and its date comes from the build timestamp.

If the first slide in your Markdown file contains only a `%% ... %%` block, that slide is treated as cover-slide notes/background metadata instead of a visible content slide.

### Notes And Metadata

Speaker notes and slide metadata both live inside `%% ... %%` blocks.
Metadata uses `key: value` syntax, not `key="value"`.

```md
# Intro

Visible content here.

%%
Say this, do not show it.
%%
```

```md
# Image background

Text on top.

%%
bg: "https://example.com/image.png"
%%

---

# Color background

More text.

%%
bg: "#f3f0ea"
%%
```

Accepted `bg` values:

- URLs
- local asset paths
- colors like `white`, `#ffffff`, `rgb(255,255,255)`, or `hsl(0 0% 100%)`

If `bg` points to a video file such as `.mp4`, `.webm`, `.ogg`, or `.mov`, it is rendered as a looping slide background.

### Templates

Templates are set in the same metadata block:

```md
# Marked slide

Some text here.

%%
template: "marked-text"
%%
```

Currently supported templates:

- `marked-text`: wraps visible text in dark highlight blocks for text-on-image slides
- `image-right`: uses the first image on the slide as a full-height right-side panel

Example:

```md
# Product overview

Short explanation here.

![[mockup.png]]

%%
template: "image-right"
%%
```

### Supported Markdown Features

- headings
- paragraphs
- unordered and ordered lists
- fenced code blocks
- inline code, emphasis, and strong text
- Markdown links
- standard Markdown images like `![Alt](./image.png)`
- Obsidian embeds like `![[image.png]]`
- raw HTML blocks

YAML frontmatter at the top of the file is ignored.

## Controls

In both the presentation and notes windows:

- `ArrowRight`, `Space`, `PageDown`: next slide
- `ArrowLeft`, `PageUp`: previous slide

In the presentation window:

- `f`: enter fullscreen
- move the pointer to the top-right corner to reveal the fullscreen button
