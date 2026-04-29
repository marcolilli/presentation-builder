# Presentation Builder

This repo is now a Go + Wails application. The management UI runs in a native Wails window, while generated presentations and notes still open in the system browser from a local Go HTTP server.

Write a presentation in one Obsidian-flavored Markdown file, then generate two standalone HTML files:

- `your-talk.html`
- `your-talk.notes.html`

The generated files inline their CSS and JavaScript. Referenced local assets are copied into the generated presentation folder automatically.

## What This Repo Contains

- `builder.go`: native presentation builder for Markdown to slides + notes HTML
- `main.go` / `app.go`: Wails app entrypoint and Go backend for the native management window
- `frontend/`: embedded Wails frontend assets
- `presentations/`: generated output folders
- `legacy/node/`: archived Node implementation kept only for reference

This repo does not currently store the source Markdown files for the sample presentations. Those are expected to live wherever you author them.

## Requirements

- Go
- Wails CLI

## Quick Start

Run the native app in dev mode:

```bash
wails dev
```

The Wails window is the manager UI. When you open slides or notes, they launch in your configured browser from a local Go HTTP server on an ephemeral loopback port.

The native app handles building, rebuilding, local asset serving, source-file watching, browser opening, and deck live reload without any Node runtime.

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

`presentation.json` stores the original absolute source path so the app can rebuild and watch the Markdown file later.

## Browser Windows

Do not open the generated files with `file://` if you want slide syncing, notes syncing, or live reload.
The presentation and notes windows communicate through `BroadcastChannel`, which needs the same origin.

The Wails app serves generated decks from an ephemeral loopback address such as:

```text
http://127.0.0.1:<port>/presentations/my-talk/my-talk.html
http://127.0.0.1:<port>/presentations/my-talk/my-talk.notes.html
```

While the app is running, it automatically watches every generated presentation with valid source metadata, rebuilds on Markdown changes, and reloads open slide and notes windows.

## Manual Build

The active workflow is the Wails manager UI. The legacy Node command-line path has been archived under `legacy/node/`.

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

Speaker-note styling can be set with `comment-properties` inside the same metadata block:

```md
%%
comment-properties: "text-color=blue"
%%
```

Currently supported comment property options:

- `text-color`: accepts named colors, hex values, or `rgb(...)` values
- `blue` is normalized to `rgb(26,23,239)`

### Presets

Presets are set in the same metadata block. You can combine multiple presets by separating them with commas:

```md
# Marked slide

Some text here.

%%
presets: "marked-text, smaller-text"
%%
```

Currently supported presets:

- `marked-text`: renders visible text as black background blocks for text-on-image slides
- `image-right`: uses the first image on the slide as a full-height right-side panel
- `center`: keeps the first `h1` in the normal top position and centers the remaining slide content
- `smaller-text`: reduces the size of body text to fit more content on a slide

Example:

```md
# Product overview

Short explanation here.

![[mockup.png]]

%%
presets: "image-right"
%%
```

### Supported Markdown Features

- headings
- paragraphs
- unordered and ordered lists
- GFM-style pipe tables
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
