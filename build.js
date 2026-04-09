#!/usr/bin/env node

const fs = require("fs");
const path = require("path");

function buildPresentation(inputArg) {
  const inputPath = path.resolve(process.cwd(), inputArg);

  if (!fs.existsSync(inputPath)) {
    throw new Error(`Input file not found: ${inputPath}`);
  }

  const source = fs.readFileSync(inputPath, "utf8");
  const builtAt = new Date().toISOString();
  const deck = parseDeck(source, inputPath, builtAt);
  const outputName = path.basename(inputPath, path.extname(inputPath));
  const outputDir = path.join(__dirname, "presentations", outputName);
  const outputBase = path.join(outputDir, outputName);
  const presentationPath = `${outputBase}.html`;
  const notesPath = `${outputBase}.notes.html`;
  const metadataPath = path.join(outputDir, "presentation.json");

  fs.mkdirSync(outputDir, { recursive: true });
  localizeDeckAssets(deck, inputPath, outputDir);
  deck.imageAssets = collectImageAssets(deck.slides);

  fs.writeFileSync(
    presentationPath,
    renderPresentationHtml(deck, path.basename(inputPath)),
    "utf8",
  );
  fs.writeFileSync(notesPath, renderNotesHtml(deck, path.basename(inputPath)), "utf8");
  fs.writeFileSync(
    metadataPath,
    JSON.stringify(
      {
        name: outputName,
        sourcePath: inputPath,
        presentationPath,
        notesPath,
        builtAt,
      },
      null,
      2,
    ),
    "utf8",
  );

  return {
    inputPath,
    outputDir,
    presentationPath,
    notesPath,
    metadataPath,
  };
}

function main() {
  const inputArg = process.argv[2];

  if (!inputArg) {
    console.error("Usage: node build.js path/to/presentation.md");
    process.exit(1);
  }

  try {
    const result = buildPresentation(inputArg);
    console.log(`Built ${result.presentationPath}`);
    console.log(`Built ${result.notesPath}`);
  } catch (error) {
    console.error(error.message);
    process.exit(1);
  }
}

function parseDeck(source, inputPath, createdAt) {
  const normalized = stripFrontmatter(source.replace(/\r\n/g, "\n"));
  const title = path.basename(inputPath, path.extname(inputPath));
  const slideSources = splitSlides(normalized);
  let coverNotesSlide = null;

  if (slideSources.length > 0 && isNotesOnlySlideSource(slideSources[0])) {
    coverNotesSlide = parseSlide(slideSources.shift(), 0, inputPath);
  }

  const slides = slideSources.map((slideSource, index) => parseSlide(slideSource, index, inputPath));
  slides.unshift(buildCoverSlide(title, createdAt, coverNotesSlide));

  slides.forEach((slide, index) => {
    slide.index = index;
  });

  return {
    channelName: makeChannelName(inputPath),
    sourceName: path.basename(inputPath),
    title,
    slides,
    imageAssets: [],
  };
}

function buildCoverSlide(title, createdAt, notesSlide = null) {
  const formattedDate = formatMonthYear(createdAt);

  return {
    index: 0,
    title,
    template: "cover",
    html: `
      <div class="cover-slide">
        <p class="cover-slide-kicker">Presentation</p>
        <h1 class="cover-slide-title">${escapeHtml(title)}</h1>
        <p class="cover-slide-date">${escapeHtml(formattedDate)}</p>
      </div>
    `.trim(),
    plainText: `${title}\n${formattedDate}`,
    notesHtml: notesSlide?.notesHtml || "",
    notesText: notesSlide?.notesText || "",
    background: notesSlide?.background || { type: "none", value: "" },
  };
}

function formatMonthYear(value) {
  return new Intl.DateTimeFormat("en-US", {
    month: "long",
    year: "numeric",
  }).format(new Date(value));
}

function isNotesOnlySlideSource(slideSource) {
  return slideSource.replace(/%%[\s\S]*?%%/g, "").trim() === "";
}

function stripFrontmatter(source) {
  return source.replace(/^---\n[\s\S]*?\n---\n*/, "");
}

function splitSlides(source) {
  return source
    .split(/\n---\n/g)
    .map((slide) => slide.trim())
    .filter(Boolean);
}

function collectImageAssets(slides) {
  const assets = new Set();

  for (const slide of slides) {
    if (slide.background && slide.background.type === "image" && slide.background.value) {
      assets.add(slide.background.value);
    }

    for (const match of slide.html.matchAll(/<img[^>]+src="([^"]+)"/g)) {
      assets.add(match[1]);
    }
  }

  return Array.from(assets);
}

function localizeDeckAssets(deck, inputPath, outputDir) {
  const assetsDir = path.join(outputDir, "assets");
  const copyCache = new Map();
  const usedNames = new Set();

  for (const slide of deck.slides) {
    slide.html = rewriteHtmlAssetSources(slide.html, inputPath, assetsDir, copyCache, usedNames);
    slide.notesHtml = rewriteHtmlAssetSources(
      slide.notesHtml,
      inputPath,
      assetsDir,
      copyCache,
      usedNames,
    );

    if (
      slide.background &&
      (slide.background.type === "image" || slide.background.type === "video")
    ) {
      slide.background.value = localizeAssetValue(
        slide.background.value,
        inputPath,
        assetsDir,
        copyCache,
        usedNames,
      );
    }
  }
}

function rewriteHtmlAssetSources(html, inputPath, assetsDir, copyCache, usedNames) {
  if (!html) {
    return html;
  }

  return html.replace(/(<(?:img|video)[^>]+\ssrc=")([^"]+)(")/g, (_, before, src, after) => {
    const localized = localizeAssetValue(src, inputPath, assetsDir, copyCache, usedNames);
    return `${before}${escapeAttribute(localized)}${after}`;
  });
}

function localizeAssetValue(value, inputPath, assetsDir, copyCache, usedNames) {
  if (!value || isRemoteAsset(value)) {
    return value;
  }

  const sourcePath = resolveLocalAssetPath(value, inputPath);

  if (!sourcePath) {
    return value;
  }

  if (copyCache.has(sourcePath)) {
    return copyCache.get(sourcePath);
  }

  fs.mkdirSync(assetsDir, { recursive: true });

  const outputName = allocateAssetName(path.basename(sourcePath), usedNames);
  const outputPath = path.join(assetsDir, outputName);
  fs.copyFileSync(sourcePath, outputPath);

  const relativePath = `assets/${outputName}`;
  copyCache.set(sourcePath, relativePath);
  return relativePath;
}

function allocateAssetName(baseName, usedNames) {
  let candidate = baseName;
  let counter = 2;
  const ext = path.extname(baseName);
  const stem = path.basename(baseName, ext);

  while (usedNames.has(candidate)) {
    candidate = `${stem}-${counter}${ext}`;
    counter += 1;
  }

  usedNames.add(candidate);
  return candidate;
}

function resolveLocalAssetPath(reference, inputPath) {
  const normalized = normalizeAssetReference(reference);

  if (!normalized) {
    return null;
  }

  if (path.isAbsolute(normalized) && fs.existsSync(normalized)) {
    return normalized;
  }

  const sourceDir = path.dirname(inputPath);
  const directPath = path.resolve(sourceDir, normalized);
  if (fs.existsSync(directPath)) {
    return directPath;
  }

  const vaultRoot = findVaultRoot(sourceDir);
  if (!vaultRoot) {
    return null;
  }

  return findFileByName(vaultRoot, path.basename(normalized));
}

function normalizeAssetReference(reference) {
  return reference.split("|")[0].trim();
}

function isRemoteAsset(value) {
  return /^(https?:)?\/\//i.test(value) || /^data:/i.test(value);
}

function findVaultRoot(startDir) {
  let currentDir = startDir;

  while (true) {
    if (fs.existsSync(path.join(currentDir, ".obsidian"))) {
      return currentDir;
    }

    const parentDir = path.dirname(currentDir);
    if (parentDir === currentDir) {
      return null;
    }
    currentDir = parentDir;
  }
}

function findFileByName(rootDir, fileName) {
  const entries = fs.readdirSync(rootDir, { withFileTypes: true });

  for (const entry of entries) {
    if (entry.name.startsWith(".")) {
      continue;
    }

    const fullPath = path.join(rootDir, entry.name);
    if (entry.isFile() && entry.name === fileName) {
      return fullPath;
    }
  }

  for (const entry of entries) {
    if (!entry.isDirectory() || entry.name.startsWith(".")) {
      continue;
    }

    const result = findFileByName(path.join(rootDir, entry.name), fileName);
    if (result) {
      return result;
    }
  }

  return null;
}

function parseSlide(slideSource, index) {
  const notesMatches = [...slideSource.matchAll(/%%([\s\S]*?)%%/g)];
  const notesBlocks = notesMatches.map((match) => match[1].trim()).filter(Boolean);
  const metadata = parseSlideMetadata(notesBlocks);
  const notesText = extractSpeakerNotes(notesBlocks).join("\n\n").trim();
  const visibleMarkdown = slideSource.replace(/%%[\s\S]*?%%/g, "").trim();
  const template = metadata.template || "";
  const html = renderSlideHtml(visibleMarkdown, template);
  const plainText = markdownToPlainText(visibleMarkdown);
  const title = getSlideTitle(visibleMarkdown, index);

  return {
    index,
    title,
    template,
    html,
    plainText,
    notesHtml: notesText ? markdownToHtml(notesText) : "",
    notesText,
    background: buildBackground(metadata.bg),
  };
}

function renderSlideHtml(markdown, template) {
  const html = markdownToHtml(markdown);

  if (template === "marked-text") {
    return applyMarkedTextTemplate(html);
  }

  if (template === "image-right") {
    return applyRightHalfImageTemplate(html);
  }

  return html;
}

function applyRightHalfImageTemplate(html) {
  const firstImageMatch = html.match(/<p>\s*(<img class="slide-media" src="([^"]+)" alt="([^"]*)" \/>?)\s*<\/p>/i);

  if (!firstImageMatch) {
    return html;
  }

  const [, imageMarkup, imageSrc, imageAlt] = firstImageMatch;
  const contentHtml = html.replace(firstImageMatch[0], "").trim();

  return `
    <div class="right-half-image-layout">
      <div class="right-half-image-copy">
        ${contentHtml}
      </div>
      <div class="right-half-image-panel" role="img" aria-label="${escapeAttribute(imageAlt)}" style="background-image:url('${escapeAttribute(imageSrc)}');"></div>
    </div>
  `.trim();
}

function extractSpeakerNotes(blocks) {
  return blocks
    .map((block) =>
      block
        .split("\n")
        .filter(
          (line) =>
            !isMetadataLine(line.trim()),
        )
        .join("\n")
        .trim(),
    )
    .filter(Boolean);
}

function parseSlideMetadata(blocks) {
  const metadata = {};

  for (const block of blocks) {
    for (const rawLine of block.split("\n")) {
      const line = rawLine.trim();

      if (!isMetadataLine(line)) {
        continue;
      }

      const separatorIndex = line.indexOf(":");
      const key = line.slice(0, separatorIndex).trim();
      const rawValue = line.slice(separatorIndex + 1).trim();
      metadata[key] = stripOptionalQuotes(rawValue);
    }
  }

  return metadata;
}

function isMetadataLine(line) {
  return /^[a-zA-Z][a-zA-Z0-9_-]*\s*:\s*.+$/.test(line);
}

function stripOptionalQuotes(value) {
  if (
    (value.startsWith('"') && value.endsWith('"')) ||
    (value.startsWith("'") && value.endsWith("'"))
  ) {
    return value.slice(1, -1);
  }

  return value;
}

function buildBackground(rawValue) {
  if (!rawValue) {
    return { type: "none", value: "" };
  }

  const value = rawValue.trim();

  if (!value) {
    return { type: "none", value: "" };
  }

  if (isColorValue(value)) {
    return { type: "color", value };
  }

  if (isVideoAsset(value)) {
    return { type: "video", value };
  }

  return { type: "image", value };
}

function isVideoAsset(value) {
  return /\.(mp4|webm|ogg|mov)(\?.*)?$/i.test(value);
}

function isColorValue(value) {
  if (/^#([0-9a-f]{3}|[0-9a-f]{4}|[0-9a-f]{6}|[0-9a-f]{8})$/i.test(value)) {
    return true;
  }

  if (/^(rgb|rgba|hsl|hsla)\(/i.test(value)) {
    return true;
  }

  return /^[a-z]+$/i.test(value);
}

function markdownToHtml(markdown) {
  if (!markdown.trim()) {
    return "";
  }

  const lines = markdown.split("\n");
  const blocks = [];
  let paragraph = [];
  let list = null;
  let inCodeBlock = false;
  let codeFence = "";
  let codeLines = [];

  const flushParagraph = () => {
    if (!paragraph.length) {
      return;
    }

    blocks.push(`<p>${inlineMarkdown(paragraph.join(" "))}</p>`);
    paragraph = [];
  };

  const flushList = () => {
    if (!list) {
      return;
    }

    const items = list.items
      .map((item) => `<li>${inlineMarkdown(item.trim())}</li>`)
      .join("");
    blocks.push(`<${list.type}>${items}</${list.type}>`);
    list = null;
  };

  const flushCodeBlock = () => {
    if (!inCodeBlock) {
      return;
    }

    const languageClass = codeFence ? ` class="language-${escapeHtml(codeFence)}"` : "";
    blocks.push(
      `<pre><code${languageClass}>${highlightCode(codeLines.join("\n"), codeFence)}</code></pre>`,
    );
    inCodeBlock = false;
    codeFence = "";
    codeLines = [];
  };

  for (const rawLine of lines) {
    const line = rawLine.replace(/\t/g, "    ");
    const trimmed = line.trim();

    if (trimmed.startsWith("```")) {
      flushParagraph();
      flushList();

      if (inCodeBlock) {
        flushCodeBlock();
      } else {
        inCodeBlock = true;
        codeFence = trimmed.slice(3).trim();
        codeLines = [];
      }

      continue;
    }

    if (inCodeBlock) {
      codeLines.push(rawLine);
      continue;
    }

    if (!trimmed) {
      flushParagraph();
      flushList();
      continue;
    }

    const headingMatch = trimmed.match(/^(#{1,6})\s+(.*)$/);
    if (headingMatch) {
      flushParagraph();
      flushList();
      const level = headingMatch[1].length;
      blocks.push(`<h${level}>${inlineMarkdown(headingMatch[2])}</h${level}>`);
      continue;
    }

    const unorderedMatch = trimmed.match(/^[-*]\s+(.*)$/);
    if (unorderedMatch) {
      flushParagraph();
      if (!list || list.type !== "ul") {
        flushList();
        list = { type: "ul", items: [] };
      }
      list.items.push(unorderedMatch[1]);
      continue;
    }

    const orderedMatch = trimmed.match(/^\d+\.\s+(.*)$/);
    if (orderedMatch) {
      flushParagraph();
      if (!list || list.type !== "ol") {
        flushList();
        list = { type: "ol", items: [] };
      }
      list.items.push(orderedMatch[1]);
      continue;
    }

    if (/^<[^>]+>/.test(trimmed)) {
      flushParagraph();
      flushList();
      blocks.push(rawLine);
      continue;
    }

    paragraph.push(trimmed);
  }

  flushParagraph();
  flushList();
  flushCodeBlock();

  return blocks.join("\n");
}

function markdownToPlainText(markdown) {
  return markdown
    .replace(/!\[\[([^\]]+)\]\]/g, "$1")
    .replace(/!\[([^\]]*)\]\(([^)]+)\)/g, "$1 $2")
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, "$1")
    .replace(/`([^`]+)`/g, "$1")
    .replace(/\*\*([^*]+)\*\*/g, "$1")
    .replace(/\*([^*]+)\*/g, "$1")
    .replace(/^#{1,6}\s+/gm, "")
    .trim();
}

function getSlideTitle(markdown, index) {
  const headingMatch = markdown.match(/^#{1,6}\s+(.+)$/m);
  if (headingMatch) {
    return markdownToPlainText(headingMatch[1]).trim();
  }

  const firstTextLine = markdown
    .split("\n")
    .map((line) => line.trim())
    .find((line) => line && !line.startsWith("![["));

  return (firstTextLine ? markdownToPlainText(firstTextLine).trim() : "") || `Slide ${index + 1}`;
}

function inlineMarkdown(source) {
  let html = escapeHtml(source);

  html = html.replace(/!\[\[([^[\]]+)\]\]/g, (_, target) => renderAsset(target.trim()));
  html = html.replace(
    /!\[([^\]]*)\]\(([^)]+)\)/g,
    (_, alt, target) => renderAsset(target.trim(), alt.trim()),
  );
  html = html.replace(
    /\[([^\]]+)\]\(([^)]+)\)/g,
    (_, label, href) =>
      `<a href="${escapeAttribute(href.trim())}" target="_blank" rel="noreferrer">${escapeHtml(
        label.trim(),
      )}</a>`,
  );
  html = html.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/\*([^*]+)\*/g, "<em>$1</em>");
  html = html.replace(/`([^`]+)`/g, "<code>$1</code>");

  return html;
}

function applyMarkedTextTemplate(html) {
  let output = "";
  let index = 0;
  const stack = [];

  while (index < html.length) {
    if (html[index] === "<") {
      const end = html.indexOf(">", index);

      if (end === -1) {
        output += html.slice(index);
        break;
      }

      const tag = html.slice(index, end + 1);
      output += tag;

      const tagMatch = tag.match(/^<\s*(\/?)\s*([a-zA-Z0-9-]+)/);
      if (tagMatch) {
        const isClosing = tagMatch[1] === "/";
        const tagName = tagMatch[2].toLowerCase();
        const isVoid =
          /\/>$/.test(tag) ||
          ["img", "br", "hr", "input", "meta", "link", "source"].includes(tagName);

        if (isClosing) {
          if (stack.length && stack[stack.length - 1] === tagName) {
            stack.pop();
          }
        } else if (!isVoid) {
          stack.push(tagName);
        }
      }

      index = end + 1;
      continue;
    }

    const nextTag = html.indexOf("<", index);
    const text = html.slice(index, nextTag === -1 ? html.length : nextTag);
    const shouldMark = text.trim() && !stack.includes("code") && !stack.includes("pre");
    output += shouldMark ? `<span class="marked">${text}</span>` : text;
    index = nextTag === -1 ? html.length : nextTag;
  }

  return output;
}

function renderAsset(target, alt = "") {
  const safeTarget = escapeAttribute(target);
  const safeAlt = escapeAttribute(alt);

  if (isVideoAsset(target)) {
    return `<video class="slide-media" src="${safeTarget}" controls playsinline preload="metadata"></video>`;
  }

  return `<img class="slide-media" src="${safeTarget}" alt="${safeAlt}" />`;
}

function escapeHtml(value) {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function escapeAttribute(value) {
  return escapeHtml(value).replace(/"/g, "&quot;");
}

function highlightCode(source, language) {
  const escaped = escapeHtml(source);
  const lowerLanguage = (language || "").toLowerCase();

  if (!lowerLanguage) {
    return escaped;
  }

  let highlighted = escaped;

  if (
    ["js", "jsx", "ts", "tsx", "javascript", "typescript", "html", "css", "json", "md", "markdown"].includes(lowerLanguage)
  ) {
    highlighted = highlighted.replace(
      /(&quot;.*?&quot;|&#39;.*?&#39;)/g,
      '<span class="token string">$1</span>',
    );
    highlighted = highlighted.replace(
      /\b(const|let|var|function|return|if|else|for|while|switch|case|break|continue|new|class|extends|import|from|export|default|async|await|try|catch|finally|throw|typeof)\b/g,
      '<span class="token keyword">$1</span>',
    );
    highlighted = highlighted.replace(
      /\b(true|false|null|undefined|NaN)\b/g,
      '<span class="token boolean">$1</span>',
    );
    highlighted = highlighted.replace(
      /\b(\d+(?:\.\d+)?)\b/g,
      '<span class="token number">$1</span>',
    );
    highlighted = highlighted.replace(
      /(&lt;!--[\s\S]*?--&gt;|\/\/.*?$|\/\*[\s\S]*?\*\/)/gm,
      '<span class="token comment">$1</span>',
    );
  }

  return highlighted;
}

function makeChannelName(inputPath) {
  const slug = inputPath
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return `presentation-${slug}`;
}

function serializeSlides(slides) {
  return JSON.stringify(
    slides.map((slide) => ({
      index: slide.index,
      title: slide.title,
      template: slide.template,
      html: slide.html,
      plainText: slide.plainText,
      notesHtml: slide.notesHtml,
      notesText: slide.notesText,
      background: slide.background,
    })),
  );
}

function renderSlideMarkup(slide, totalSlides) {
  const background = renderSlideBackground(slide.background);
  const templateClass = slide.template ? ` template-${escapeAttribute(slide.template)}` : "";

  return `
    <section class="slide${templateClass}" data-slide-index="${slide.index}" aria-label="Slide ${slide.index + 1} of ${totalSlides}">
      ${background}
      <div class="slide-content">
        ${slide.html}
      </div>
    </section>
  `.trim();
}

function renderSlideBackground(background) {
  if (!background || background.type === "none") {
    return "";
  }

  if (background.type === "color") {
    return `<div class="slide-background slide-background-color" style="background:${escapeAttribute(
      background.value,
    )};"></div>`;
  }

  if (background.type === "video") {
    return `<video class="slide-background slide-background-video" src="${escapeAttribute(
      background.value,
    )}" autoplay muted loop playsinline></video>`;
  }

  return `<div class="slide-background slide-background-image" style="background-image:url('${escapeAttribute(
    background.value,
  )}');"></div>`;
}

function renderSharedStyles() {
  return `
    :root {
      --slide-padding-x: 4.5rem;
      --slide-padding-y: 3rem;
      --headline-font: "Arial Narrow", Arial, sans-serif;
      --body-font: Inter, "Helvetica Neue", Helvetica, Arial, sans-serif;
      --text-color: #111111;
      --muted-color: #5b5b5b;
      --border-color: rgba(17, 17, 17, 0.12);
      --panel-color: rgba(255, 255, 255, 0.92);
      --shadow-color: rgba(0, 0, 0, 0.08);
    }

    * {
      box-sizing: border-box;
    }

    html,
    body {
      width: 100%;
      height: 100%;
      margin: 0;
      background: #ffffff;
      color: var(--text-color);
      font-family: var(--body-font);
    }

    body {
      overflow: hidden;
    }

    h1,
    h2,
    h3,
    h4,
    h5,
    h6 {
      margin: 0 0 1rem;
      font-family: var(--headline-font);
      font-weight: 100;
      line-height: 0.95;
      letter-spacing: -0.03em;
    }

    h1 {
      font-size: clamp(3.2rem, 7vw, 6.6rem);
    }

    h2 {
      font-size: clamp(2.6rem, 5vw, 4.8rem);
    }

    h3 {
      font-size: clamp(2rem, 4vw, 3.4rem);
    }

    p,
    li,
    blockquote,
    code,
    center {
      font-size: clamp(2.05rem, 2.7vw, 2.55rem);
      line-height: 1.45;
    }

    center {
      display: block;
    }

    p,
    ul,
    ol,
    pre,
    blockquote {
      margin: 0 0 1rem;
    }

    ul,
    ol {
      padding-left: 1.35em;
    }

    a {
      color: #1557ff;
      text-decoration-thickness: 0.08em;
      text-underline-offset: 0.14em;
    }

    code {
      font-family: "SFMono-Regular", Menlo, Consolas, monospace;
    }

    :not(pre) > code {
      padding: 0.08em 0.28em;
      background: #f3f3f3;
    }

    pre {
      overflow-x: auto;
      overflow-y: visible;
      padding: 1rem 1.2rem;
      border: 0.0625rem solid var(--border-color);
      background: #f3f3f3;
      white-space: pre-wrap;
      overflow-wrap: anywhere;
    }

    img,
    video {
      max-width: 100%;
    }

    .slide-media {
      display: block;
      width: auto;
      max-width: min(100%, 78rem);
      max-height: 58vh;
      object-fit: contain;
      margin: 1rem 0;
    }

    .slide-deck {
      width: 100vw;
      height: 100vh;
      position: relative;
    }

    .slide {
      position: absolute;
      inset: 0;
      display: none;
      padding: var(--slide-padding-y) var(--slide-padding-x);
      background: #ffffff;
      overflow-x: hidden;
      overflow-y: auto;
      isolation: isolate;
    }

    .slide.is-active {
      display: block;
    }

    .slide-background {
      position: absolute;
      inset: 0;
      z-index: -1;
    }

    .slide-background-image {
      background-position: center;
      background-repeat: no-repeat;
      background-size: cover;
    }

    .slide-background-video {
      width: 100%;
      height: 100%;
      object-fit: cover;
    }

    .slide-content {
      position: relative;
      width: 100%;
      max-width: 90rem;
      margin: 0 auto;
      text-align: left;
      padding-bottom: 3rem;
    }

    .slide.template-cover {
      display: none;
      align-items: flex-end;
      background:
        radial-gradient(circle at top left, rgba(21, 87, 255, 0.14), transparent 28%),
        linear-gradient(180deg, #faf7f1 0%, #ffffff 48%, #f2f5ff 100%);
    }

    .slide.template-cover.is-active {
      display: flex;
    }

    .slide.template-cover .slide-content {
      display: flex;
      align-items: flex-end;
      min-height: 100%;
      max-width: 100%;
      padding-bottom: 0;
    }

    .cover-slide {
      width: min(100%, 64rem);
      padding: 0 0 4vh;
    }

    .cover-slide-kicker {
      margin-bottom: 1.2rem;
      font-size: 0.95rem;
      letter-spacing: 0.18em;
      text-transform: uppercase;
      color: var(--muted-color);
    }

    .cover-slide-title {
      margin-bottom: 1.5rem;
    }

    .cover-slide-date {
      font-size: clamp(1.1rem, 2vw, 1.5rem);
      color: var(--muted-color);
    }

    .slide.template-image-right {
      padding-top: 0;
      padding-bottom: 0;
      padding-left: 0;
      padding-right: 0;
    }

    .slide.template-image-right .slide-content {
      max-width: 100%;
      min-height: 100vh;
      margin: 0;
      padding-bottom: 0;
    }

    .right-half-image-layout {
      display: grid;
      grid-template-columns: minmax(0, 1fr) 50%;
      min-height: 100vh;
    }

    .right-half-image-copy {
      padding: var(--slide-padding-y) var(--slide-padding-x);
      align-self: center;
    }

    .right-half-image-panel {
      min-height: 100vh;
      background-position: center center;
      background-repeat: no-repeat;
      background-size: cover;
    }

    .slide.template-marked-text,
    .slide.template-marked-text a,
    .slide.template-marked-text h1,
    .slide.template-marked-text h2,
    .slide.template-marked-text h3,
    .slide.template-marked-text h4,
    .slide.template-marked-text h5,
    .slide.template-marked-text h6,
    .slide.template-marked-text p,
    .slide.template-marked-text li,
    .slide.template-marked-text blockquote,
    .slide.template-marked-text center {
      color: #ffffff;
    }

    .marked {
      background-color: #000000;
      color: #ffffff;
      padding: 0.1em 0.2em;
      line-height: 1.5em;
      box-decoration-break: clone;
      -webkit-box-decoration-break: clone;
    }

    ul,
    ol {
      list-style: none;
      padding-left: 0;
    }

    li {
      position: relative;
      padding-left: 1.2em;
    }

    li::before {
      content: "-";
      position: absolute;
      left: 0;
      color: currentColor;
    }

    .token.comment {
      color: #6b7280;
    }

    .token.keyword {
      color: #9a3412;
      font-weight: 600;
    }

    .token.string {
      color: #1d4ed8;
    }

    .token.number,
    .token.boolean {
      color: #047857;
    }

    .slide-number {
      position: fixed;
      right: 1rem;
      bottom: 0.8rem;
      font-size: 0.85rem;
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: rgba(17, 17, 17, 0.56);
      z-index: 20;
    }

    .fullscreen-toggle {
      appearance: none;
      position: fixed;
      top: 1rem;
      right: 1rem;
      border: 0.0625rem solid var(--border-color);
      background: rgba(255, 255, 255, 0.92);
      color: var(--text-color);
      padding: 0.55rem 0.8rem;
      font: inherit;
      font-size: 0.8rem;
      letter-spacing: 0.08em;
      text-transform: uppercase;
      cursor: pointer;
      z-index: 20;
      opacity: 0;
      pointer-events: none;
      transition: opacity 120ms ease;
    }

    .fullscreen-toggle.is-visible {
      opacity: 1;
      pointer-events: auto;
    }

    .fullscreen-toggle:hover {
      background: #ffffff;
    }

    .presentation-title {
      position: fixed;
      left: 1rem;
      bottom: 0.8rem;
      font-size: 0.85rem;
      letter-spacing: 0.08em;
      text-transform: uppercase;
      color: rgba(17, 17, 17, 0.56);
      z-index: 20;
    }

    @media (max-width: 56.25rem) {
      :root {
        --slide-padding-x: 1.4rem;
        --slide-padding-y: 1.6rem;
      }

      .slide-media {
        max-height: 44vh;
      }

      .slide.template-image-right {
        padding: 0;
      }

      .slide.template-image-right .slide-content,
      .right-half-image-layout,
      .right-half-image-panel {
        min-height: auto;
      }

      .right-half-image-layout {
        grid-template-columns: 1fr;
        gap: 1rem;
      }

      .right-half-image-copy {
        padding-right: 0;
      }

      .right-half-image-panel {
        min-height: 35vh;
      }
    }
  `;
}

function renderPresentationHtml(deck, sourceName) {
  const slidesMarkup = deck.slides.map((slide) => renderSlideMarkup(slide, deck.slides.length)).join("\n");
  const slidesJson = serializeSlides(deck.slides);
  const prefetchMarkup = deck.imageAssets
    .map(
      (asset) =>
        `    <link rel="prefetch" as="image" href="${escapeAttribute(asset)}" />`,
    )
    .join("\n");

  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>${escapeHtml(deck.title)}</title>
${prefetchMarkup}
    <style>
${renderSharedStyles()}
    </style>
  </head>
  <body>
    <main class="slide-deck" id="deck">
      ${slidesMarkup}
    </main>
    <button class="fullscreen-toggle" id="fullscreen-toggle" type="button">Fullscreen</button>
    <div class="presentation-title">${escapeHtml(deck.title)}</div>
    <div class="slide-number" id="slide-number"></div>
    <script>
      const CHANNEL_NAME = ${JSON.stringify(deck.channelName)};
      const SLIDES = ${slidesJson};
      const IMAGE_ASSETS = ${JSON.stringify(deck.imageAssets)};
      const params = new URLSearchParams(window.location.search);
      const isPreview = params.get("preview") === "1";
      const liveReloadEnabled = params.get("liveReload") === "1";
      const presentationName = ${JSON.stringify(deck.title)};
      const channel = isPreview ? null : new BroadcastChannel(CHANNEL_NAME);
      const slideElements = Array.from(document.querySelectorAll(".slide"));
      const slideNumber = document.getElementById("slide-number");
      const fullscreenToggle = document.getElementById("fullscreen-toggle");
      let currentIndex = 0;

      function readIndexFromQuery() {
        const params = new URLSearchParams(window.location.search);
        const rawValue = params.get("slide");
        const parsed = Number(rawValue);

        if (!Number.isInteger(parsed)) {
          return 0;
        }

        return parsed - 1;
      }

      function writeIndexToQuery(index) {
        const url = new URL(window.location.href);
        url.searchParams.set("slide", String(index + 1));
        window.history.replaceState({}, "", url);
      }

      function clampIndex(index) {
        return Math.max(0, Math.min(index, slideElements.length - 1));
      }

      function prefetchImages() {
        IMAGE_ASSETS.forEach((asset) => {
          const image = new Image();
          image.decoding = "async";
          image.src = asset;
        });
      }

      function setupLiveReload() {
        if (!liveReloadEnabled || isPreview || window.location.protocol !== "http:") {
          return;
        }

        const eventsUrl = new URL("/events", window.location.origin);
        eventsUrl.searchParams.set("name", presentationName);

        const source = new EventSource(eventsUrl);
        source.addEventListener("reload", () => {
          window.location.reload();
        });
      }

      function canToggleFullscreen() {
        return typeof document.documentElement.requestFullscreen === "function";
      }

      function shouldShowFullscreenButton(event) {
        return window.innerWidth - event.clientX <= 120 && event.clientY <= 88;
      }

      function updateFullscreenButton() {
        if (!fullscreenToggle) {
          return;
        }

        if (isPreview || !canToggleFullscreen()) {
          fullscreenToggle.style.display = "none";
          fullscreenToggle.classList.remove("is-visible");
          return;
        }

        fullscreenToggle.style.display = "";
        fullscreenToggle.textContent = document.fullscreenElement ? "Exit Fullscreen" : "Fullscreen";
      }

      async function enterFullscreen() {
        if (!canToggleFullscreen()) {
          return;
        }

        if (!document.fullscreenElement) {
          await document.documentElement.requestFullscreen();
        }
      }

      async function exitFullscreen() {
        if (document.fullscreenElement) {
          await document.exitFullscreen();
        }
      }

      async function toggleFullscreen() {
        if (document.fullscreenElement) {
          await exitFullscreen();
        } else {
          await enterFullscreen();
        }
      }

      function postState() {
        if (!channel) {
          return;
        }

        channel.postMessage({
          type: "state",
          source: "presentation",
          index: currentIndex,
          slides: SLIDES.length,
          current: SLIDES[currentIndex] || null,
          next: SLIDES[currentIndex + 1] || null
        });
      }

      function render(index, shouldBroadcast = true) {
        currentIndex = clampIndex(index);
        writeIndexToQuery(currentIndex);
        slideElements.forEach((slide, slideIndex) => {
          slide.classList.toggle("is-active", slideIndex === currentIndex);
        });
        slideNumber.textContent = (currentIndex + 1) + " / " + slideElements.length;
        slideNumber.style.display = isPreview ? "none" : "";
        document.title = (SLIDES[currentIndex]?.title || ${JSON.stringify(
          deck.title,
        )}) + " | " + ${JSON.stringify(sourceName)};

        if (shouldBroadcast && !isPreview) {
          postState();
        }
      }

      function goTo(index) {
        render(index, true);
      }

      function step(direction) {
        goTo(currentIndex + direction);
      }

      if (!isPreview) {
        document.addEventListener("keydown", (event) => {
          if (event.key === "ArrowRight" || event.key === " " || event.key === "PageDown") {
            event.preventDefault();
            step(1);
          } else if (event.key === "ArrowLeft" || event.key === "PageUp") {
            event.preventDefault();
            step(-1);
          } else if (event.key.toLowerCase() === "f" && !document.fullscreenElement) {
            event.preventDefault();
            enterFullscreen().catch(() => {});
          }
        });

        channel.addEventListener("message", (event) => {
          const message = event.data || {};
          if (message.source === "presentation") {
            return;
          }

          if (message.type === "request-state") {
            postState();
          } else if (message.type === "navigate") {
            if (typeof message.index === "number") {
              render(message.index, true);
            } else if (typeof message.direction === "number") {
              render(currentIndex + message.direction, true);
            }
          }
        });
      }

      window.addEventListener("load", () => {
        render(readIndexFromQuery(), !isPreview);
        if (!isPreview) {
          prefetchImages();
        }
        setupLiveReload();
        updateFullscreenButton();
      });

      if (fullscreenToggle) {
        fullscreenToggle.addEventListener("click", () => {
          toggleFullscreen().catch(() => {});
        });
      }

      document.addEventListener("mousemove", (event) => {
        if (!fullscreenToggle || isPreview || !canToggleFullscreen()) {
          return;
        }

        fullscreenToggle.classList.toggle("is-visible", shouldShowFullscreenButton(event));
      });

      document.addEventListener("fullscreenchange", updateFullscreenButton);
    </script>
  </body>
</html>
`;
}

function renderNotesHtml(deck, sourceName) {
  const slidesJson = serializeSlides(deck.slides);

  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>${escapeHtml(deck.title)} Notes</title>
    <style>
${renderSharedStyles()}

      body {
        overflow: auto;
        padding: 1rem;
        background: #f5f3ef;
      }

      .notes-layout {
        display: grid;
        grid-template-columns: minmax(0, 1fr) minmax(17.5rem, 34%);
        gap: 1rem;
        min-height: calc(100vh - 2rem);
      }

      .notes-main,
      .notes-sidebar,
      .notes-panel {
        display: flex;
        flex-direction: column;
        gap: 1rem;
      }

      .notes-panel {
        padding: 1rem;
        border: 0.0625rem solid var(--border-color);
        background: var(--panel-color);
        box-shadow: 0 0.75rem 1.875rem var(--shadow-color);
      }

      .notes-panel-next {
        max-width: 34rem;
      }

      .notes-label {
        margin: 0 0 0.4rem;
        font-size: 0.8rem;
        font-weight: 600;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--muted-color);
      }

      .notes-meta {
        display: flex;
        gap: 1rem;
        flex-wrap: wrap;
        font-size: 0.95rem;
      }

      .notes-controls {
        display: flex;
        gap: 0.75rem;
        flex-wrap: wrap;
      }

      .notes-button {
        appearance: none;
        border: 0.0625rem solid var(--border-color);
        background: #ffffff;
        color: var(--text-color);
        padding: 0.6rem 0.9rem;
        font: inherit;
        cursor: pointer;
      }

      .notes-button:hover {
        background: #f5f5f5;
      }

      .notes-preview {
        aspect-ratio: 16 / 9;
        overflow: hidden;
        position: relative;
        background: #ffffff;
        border: 0.0625rem solid var(--border-color);
      }

      .notes-preview-next {
        max-width: 32rem;
      }

      .notes-preview iframe {
        width: 100%;
        height: 100%;
        border: 0;
        background: #ffffff;
      }

      .notes-empty {
        color: var(--muted-color);
      }

      @media (max-width: 62.5rem) {
        .notes-layout {
          grid-template-columns: 1fr;
        }
      }
    </style>
  </head>
  <body>
    <div class="notes-layout">
      <section class="notes-main">
        <article class="notes-panel">
          <p class="notes-label">Current Slide</p>
          <div class="notes-meta" id="current-meta"></div>
          <div class="notes-controls">
            <button class="notes-button" id="prev-button" type="button">Previous</button>
            <button class="notes-button" id="next-button" type="button">Next</button>
          </div>
          <div class="notes-preview" id="current-slide"></div>
        </article>
        <article class="notes-panel notes-panel-next">
          <p class="notes-label">Next Slide</p>
          <div class="notes-preview notes-preview-next" id="next-slide"></div>
        </article>
      </section>
      <aside class="notes-sidebar">
        <article class="notes-panel">
          <p class="notes-label">Notes</p>
          <div id="notes"></div>
        </article>
      </aside>
    </div>
    <script>
      const CHANNEL_NAME = ${JSON.stringify(deck.channelName)};
      const SLIDES = ${slidesJson};
      const params = new URLSearchParams(window.location.search);
      const liveReloadEnabled = params.get("liveReload") === "1";
      const presentationName = ${JSON.stringify(deck.title)};
      const channel = new BroadcastChannel(CHANNEL_NAME);
      const currentMeta = document.getElementById("current-meta");
      const currentSlide = document.getElementById("current-slide");
      const nextSlide = document.getElementById("next-slide");
      const notes = document.getElementById("notes");
      const prevButton = document.getElementById("prev-button");
      const nextButton = document.getElementById("next-button");
      const presentationPath = ${JSON.stringify(sourceName.replace(/\.md$/i, ".html"))};
      let currentIndex = 0;

      function readIndexFromQuery() {
        const params = new URLSearchParams(window.location.search);
        const rawValue = params.get("slide");
        const parsed = Number(rawValue);

        if (!Number.isInteger(parsed)) {
          return 0;
        }

        return parsed - 1;
      }

      function writeIndexToQuery(index) {
        const url = new URL(window.location.href);
        url.searchParams.set("slide", String(index + 1));
        window.history.replaceState({}, "", url);
      }

      function setupLiveReload() {
        if (!liveReloadEnabled || window.location.protocol !== "http:") {
          return;
        }

        const eventsUrl = new URL("/events", window.location.origin);
        eventsUrl.searchParams.set("name", presentationName);

        const source = new EventSource(eventsUrl);
        source.addEventListener("reload", () => {
          window.location.reload();
        });
      }

      function renderSlidePreview(container, slide) {
        if (!slide) {
          container.innerHTML = '<div class="notes-empty">No slide</div>';
          return;
        }

        const url = new URL(presentationPath, window.location.href);
        url.searchParams.set("slide", String(slide.index + 1));
        url.searchParams.set("preview", "1");
        if (liveReloadEnabled) {
          url.searchParams.set("liveReload", "1");
        }
        container.innerHTML = '<iframe loading="eager" referrerpolicy="no-referrer" src="' + url.toString() + '"></iframe>';
      }

      function renderNotesPanel(slide) {
        if (!slide || !slide.notesHtml) {
          notes.innerHTML = '<div class="notes-empty">No notes for this slide.</div>';
          return;
        }

        notes.innerHTML = slide.notesHtml;
      }

      function render(index) {
        currentIndex = Math.max(0, Math.min(index, SLIDES.length - 1));
        writeIndexToQuery(currentIndex);
        const current = SLIDES[currentIndex] || null;
        const next = SLIDES[currentIndex + 1] || null;

        currentMeta.innerHTML =
          '<strong>' +
          (currentIndex + 1) +
          ' / ' +
          SLIDES.length +
          '</strong><span>' +
          (current ? current.title : '') +
          '</span>';

        renderSlidePreview(currentSlide, current);
        renderSlidePreview(nextSlide, next);
        renderNotesPanel(current);
        prevButton.disabled = currentIndex <= 0;
        nextButton.disabled = currentIndex >= SLIDES.length - 1;
        document.title = (current?.title || ${JSON.stringify(
          deck.title,
        )}) + ' Notes | ' + ${JSON.stringify(sourceName)};
      }

      prevButton.addEventListener("click", () => {
        channel.postMessage({ type: "navigate", source: "notes", direction: -1 });
      });

      nextButton.addEventListener("click", () => {
        channel.postMessage({ type: "navigate", source: "notes", direction: 1 });
      });

      document.addEventListener("keydown", (event) => {
        if (event.key === "ArrowRight" || event.key === " " || event.key === "PageDown") {
          event.preventDefault();
          channel.postMessage({ type: "navigate", source: "notes", direction: 1 });
        } else if (event.key === "ArrowLeft" || event.key === "PageUp") {
          event.preventDefault();
          channel.postMessage({ type: "navigate", source: "notes", direction: -1 });
        }
      });

      channel.addEventListener("message", (event) => {
        const message = event.data || {};
        if (message.type === "state" && typeof message.index === "number") {
          render(message.index);
        }
      });

      window.addEventListener("load", () => {
        render(readIndexFromQuery());
        channel.postMessage({ type: "request-state", source: "notes" });
        setupLiveReload();
      });
    </script>
  </body>
</html>
`;
}

module.exports = {
  buildPresentation,
};

if (require.main === module) {
  main();
}
