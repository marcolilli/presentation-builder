#!/usr/bin/env node

const http = require("http");
const fs = require("fs");
const path = require("path");
const { buildPresentation } = require("./build");

const rootDir = __dirname;
const presentationsDir = path.join(rootDir, "presentations");
const host = "127.0.0.1";
const port = Number(process.env.PORT || 4747);
const watchers = new Map();
const eventClients = new Map();

const contentTypes = {
  ".css": "text/css; charset=utf-8",
  ".gif": "image/gif",
  ".htm": "text/html; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".jpeg": "image/jpeg",
  ".jpg": "image/jpeg",
  ".js": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".md": "text/markdown; charset=utf-8",
  ".mp4": "video/mp4",
  ".ogg": "video/ogg",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".txt": "text/plain; charset=utf-8",
  ".webm": "video/webm",
  ".webp": "image/webp",
};

const server = http.createServer(async (request, response) => {
  const requestUrl = new URL(request.url, `http://${host}:${port}`);

  if (request.method === "GET" && requestUrl.pathname === "/") {
    return sendHtml(response, renderHomePage(requestUrl));
  }

  if (request.method === "GET" && requestUrl.pathname === "/events") {
    return handleEvents(request, response, requestUrl);
  }

  if (request.method === "POST" && requestUrl.pathname === "/build") {
    return handleBuild(request, response);
  }

  if (request.method === "POST" && requestUrl.pathname === "/rebuild") {
    return handleRebuild(request, response);
  }

  if (request.method === "POST" && requestUrl.pathname === "/watch") {
    return handleWatchToggle(request, response);
  }

  return serveStaticFile(requestUrl, response);
});

function handleBuild(request, response) {
  readFormBody(request)
    .then((params) => {
      const sourcePath = (params.get("sourcePath") || "").trim();

      if (!sourcePath) {
        redirectWithMessage(response, "Source path is required.");
        return;
      }

      try {
        const result = buildPresentation(sourcePath);
        redirectHome(response);
      } catch (error) {
        redirectWithMessage(response, error.message);
      }
    })
    .catch(() => {
      redirectWithMessage(response, "Could not read request body.");
    });
}

function handleRebuild(request, response) {
  readFormBody(request)
    .then((params) => {
      const name = (params.get("name") || "").trim();

      if (!name) {
        redirectWithMessage(response, "Presentation name is required.");
        return;
      }

      const metadataPath = path.join(presentationsDir, name, "presentation.json");

      if (!fs.existsSync(metadataPath)) {
        redirectWithMessage(
          response,
          `No presentation metadata found for ${name}. Rebuild is only available after building through this tool.`,
        );
        return;
      }

      const metadata = JSON.parse(fs.readFileSync(metadataPath, "utf8"));

      try {
        const result = buildPresentation(metadata.sourcePath);
        notifyReload(name);
        redirectHome(response);
      } catch (error) {
        redirectWithMessage(response, error.message);
      }
    })
    .catch(() => {
      redirectWithMessage(response, "Could not read request body.");
    });
}

function handleWatchToggle(request, response) {
  readFormBody(request)
    .then((params) => {
      const name = (params.get("name") || "").trim();

      if (!name) {
        redirectWithMessage(response, "Presentation name is required.");
        return;
      }

      if (watchers.has(name)) {
        stopWatcher(name);
        redirectHome(response);
        return;
      }

      const metadataPath = path.join(presentationsDir, name, "presentation.json");
      if (!fs.existsSync(metadataPath)) {
        redirectWithMessage(response, `No presentation metadata found for ${name}.`);
        return;
      }

      const metadata = JSON.parse(fs.readFileSync(metadataPath, "utf8"));
      if (!metadata.sourcePath || !fs.existsSync(metadata.sourcePath)) {
        redirectWithMessage(response, `Source file not found for ${name}.`);
        return;
      }

      startWatcher(name, metadata.sourcePath);
      redirectHome(response);
    })
    .catch(() => {
      redirectWithMessage(response, "Could not read request body.");
    });
}

function handleEvents(request, response, requestUrl) {
  const name = (requestUrl.searchParams.get("name") || "").trim();

  if (!name) {
    response.writeHead(400, { "Content-Type": "text/plain; charset=utf-8" });
    response.end("Missing presentation name");
    return;
  }

  response.writeHead(200, {
    "Content-Type": "text/event-stream; charset=utf-8",
    "Cache-Control": "no-store",
    Connection: "keep-alive",
  });
  response.write(`event: connected\ndata: ${JSON.stringify({ name })}\n\n`);

  if (!eventClients.has(name)) {
    eventClients.set(name, new Set());
  }

  const clients = eventClients.get(name);
  clients.add(response);

  request.on("close", () => {
    clients.delete(response);
    if (!clients.size) {
      eventClients.delete(name);
    }
  });
}

function serveStaticFile(requestUrl, response) {
  const safePath = decodeURIComponent(requestUrl.pathname);
  const joinedPath = path.join(rootDir, safePath);
  const resolvedPath = path.resolve(joinedPath);

  if (!resolvedPath.startsWith(rootDir)) {
    response.writeHead(403, { "Content-Type": "text/plain; charset=utf-8" });
    response.end("Forbidden");
    return;
  }

  let filePath = resolvedPath;

  if (fs.existsSync(filePath) && fs.statSync(filePath).isDirectory()) {
    filePath = path.join(filePath, "index.html");
  }

  fs.readFile(filePath, (error, buffer) => {
    if (error) {
      response.writeHead(error.code === "ENOENT" ? 404 : 500, {
        "Content-Type": "text/plain; charset=utf-8",
      });
      response.end(error.code === "ENOENT" ? "Not found" : "Server error");
      return;
    }

    const ext = path.extname(filePath).toLowerCase();
    response.writeHead(200, {
      "Content-Type": contentTypes[ext] || "application/octet-stream",
      "Cache-Control": "no-store",
    });
    response.end(buffer);
  });
}

function listPresentations() {
  if (!fs.existsSync(presentationsDir)) {
    return [];
  }

  return fs
    .readdirSync(presentationsDir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => {
      const name = entry.name;
      const dir = path.join(presentationsDir, name);
      const htmlPath = path.join(dir, `${name}.html`);
      const notesPath = path.join(dir, `${name}.notes.html`);
      const metadataPath = path.join(dir, "presentation.json");
      const hasPresentation = fs.existsSync(htmlPath);
      const hasNotes = fs.existsSync(notesPath);
      const metadata = fs.existsSync(metadataPath)
        ? JSON.parse(fs.readFileSync(metadataPath, "utf8"))
        : null;

      if (!hasPresentation || !hasNotes) {
        return null;
      }

      return {
        name,
        deckHref: `/presentations/${encodeURIComponent(name)}/${encodeURIComponent(name)}.html`,
        notesHref: `/presentations/${encodeURIComponent(name)}/${encodeURIComponent(name)}.notes.html`,
        deckLiveHref: `/presentations/${encodeURIComponent(name)}/${encodeURIComponent(name)}.html?liveReload=1`,
        notesLiveHref: `/presentations/${encodeURIComponent(name)}/${encodeURIComponent(name)}.notes.html?liveReload=1`,
        sourcePath: metadata ? metadata.sourcePath : "",
        builtAt: metadata ? metadata.builtAt : "",
        canRebuild: Boolean(metadata && metadata.sourcePath),
        liveReloadActive: watchers.has(name),
      };
    })
    .filter(Boolean)
    .sort((a, b) => a.name.localeCompare(b.name));
}

function renderHomePage(requestUrl) {
  const message = requestUrl.searchParams.get("message") || "";
  const presentations = listPresentations();

  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Presentations</title>
    <style>
      * { box-sizing: border-box; }
      html, body {
        margin: 0;
        font-family: Inter, "Helvetica Neue", Helvetica, Arial, sans-serif;
        background: #ffffff;
        color: #111111;
      }
      body {
        padding: 2rem;
      }
      main {
        max-width: 70rem;
        margin: 0 auto;
      }
      h1, h2 {
        font-family: "Arial Narrow", Arial, sans-serif;
        font-weight: 100;
        margin: 0 0 1rem;
      }
      h1 {
        font-size: 3rem;
      }
      h2 {
        font-size: 2rem;
        margin-top: 2rem;
      }
      p {
        line-height: 1.5;
      }
      a {
        color: #1557ff;
      }
      form {
        display: flex;
        gap: 0.75rem;
        flex-wrap: wrap;
        margin: 1rem 0 2rem;
      }
      input[type="text"] {
        flex: 1 1 28rem;
        min-width: 16rem;
        padding: 0.75rem 0.9rem;
        border: 0.0625rem solid rgba(17, 17, 17, 0.16);
        font: inherit;
      }
      button {
        appearance: none;
        border: 0.0625rem solid rgba(17, 17, 17, 0.16);
        background: #111111;
        color: #ffffff;
        padding: 0.75rem 1rem;
        font: inherit;
        cursor: pointer;
      }
      .secondary-button {
        background: #ffffff;
        color: #111111;
      }
      .message {
        margin: 0 0 1.5rem;
        padding: 0.85rem 1rem;
        background: #f3f3f3;
      }
      .presentation-list {
        display: grid;
        gap: 1rem;
      }
      .presentation-card {
        padding: 1rem;
        border: 0.0625rem solid rgba(17, 17, 17, 0.12);
      }
      .presentation-links {
        display: flex;
        gap: 1rem;
        flex-wrap: wrap;
        margin: 0.75rem 0;
      }
      .presentation-meta {
        color: #5b5b5b;
        font-size: 0.95rem;
      }
      .empty {
        color: #5b5b5b;
      }
      .toggle-form {
        margin-top: 1rem;
      }
      .toggle-label {
        display: inline-flex;
        align-items: center;
        gap: 0.6rem;
        cursor: pointer;
      }
      .toggle-label input {
        margin: 0;
      }
    </style>
  </head>
  <body>
    <main>
      <h1>Presentations</h1>
      <p>Build a presentation from a Markdown file, or reopen and rebuild an existing one.</p>
      ${message ? `<div class="message">${escapeHtml(message)}</div>` : ""}
      <h2>Build</h2>
      <form action="/build" method="post">
        <input type="text" name="sourcePath" placeholder="/absolute/path/to/presentation.md" />
        <button type="submit">Build</button>
      </form>
      <h2>Presentations</h2>
      ${
        presentations.length
          ? `<div class="presentation-list">
              ${presentations
                .map(
                  (presentation) => `<section class="presentation-card">
                    <h3>${escapeHtml(presentation.name)}</h3>
                    <div class="presentation-links">
                      <a href="${
                        presentation.liveReloadActive
                          ? presentation.deckLiveHref
                          : presentation.deckHref
                      }" target="_blank" rel="noreferrer">Open slides</a>
                      <a href="${
                        presentation.liveReloadActive
                          ? presentation.notesLiveHref
                          : presentation.notesHref
                      }" target="_blank" rel="noreferrer">Open notes</a>
                    </div>
                    <div class="presentation-meta">
                      ${
                        presentation.sourcePath
                          ? `Source: ${escapeHtml(presentation.sourcePath)}`
                          : "Source unknown"
                      }
                    </div>
                    <div class="presentation-meta">
                      ${
                        presentation.builtAt
                          ? `Last built: ${escapeHtml(presentation.builtAt)}`
                          : "Build time unknown"
                      }
                    </div>
                    ${
                      presentation.canRebuild
                        ? `<form action="/rebuild" method="post">
                            <input type="hidden" name="name" value="${escapeAttribute(
                              presentation.name,
                            )}" />
                            <button class="secondary-button" type="submit">Rebuild</button>
                          </form>`
                        : ""
                    }
                    ${
                      presentation.canRebuild
                        ? `<form class="toggle-form" action="/watch" method="post">
                            <input type="hidden" name="name" value="${escapeAttribute(
                              presentation.name,
                            )}" />
                            <label class="toggle-label">
                              <input type="checkbox" name="enabled" value="1" ${
                                presentation.liveReloadActive ? "checked" : ""
                              } onchange="this.form.submit()" />
                              <span>Live reload</span>
                            </label>
                          </form>`
                        : ""
                    }
                  </section>`,
                )
                .join("")}
            </div>`
          : `<p class="empty">No generated presentations found yet.</p>`
      }
    </main>
  </body>
</html>`;
}

function readFormBody(request) {
  return new Promise((resolve, reject) => {
    let body = "";

    request.on("data", (chunk) => {
      body += chunk.toString("utf8");
    });

    request.on("end", () => {
      resolve(new URLSearchParams(body));
    });

    request.on("error", reject);
  });
}

function startWatcher(name, sourcePath) {
  stopWatcher(name);

  const state = {
    sourcePath,
    debounceTimer: null,
    isBuilding: false,
    pending: false,
    watcher: null,
  };

  const scheduleBuild = () => {
    clearTimeout(state.debounceTimer);
    state.debounceTimer = setTimeout(() => {
      runWatchedBuild(name, state);
    }, 120);
  };

  state.watcher = fs.watch(sourcePath, scheduleBuild);
  watchers.set(name, state);
}

function stopWatcher(name) {
  const state = watchers.get(name);
  if (!state) {
    return;
  }

  clearTimeout(state.debounceTimer);
  state.watcher.close();
  watchers.delete(name);
}

function runWatchedBuild(name, state) {
  if (state.isBuilding) {
    state.pending = true;
    return;
  }

  state.isBuilding = true;

  try {
    buildPresentation(state.sourcePath);
    notifyReload(name);
  } catch (error) {
    console.error(error.message);
  } finally {
    state.isBuilding = false;
    if (state.pending) {
      state.pending = false;
      runWatchedBuild(name, state);
    }
  }
}

function notifyReload(name) {
  const clients = eventClients.get(name);
  if (!clients) {
    return;
  }

  for (const response of clients) {
    response.write(`event: reload\ndata: ${JSON.stringify({ name })}\n\n`);
  }
}

function redirectWithMessage(response, message) {
  const location = `/?message=${encodeURIComponent(message)}`;
  response.writeHead(303, { Location: location });
  response.end();
}

function redirectHome(response) {
  response.writeHead(303, { Location: "/" });
  response.end();
}

function sendHtml(response, html) {
  response.writeHead(200, {
    "Content-Type": "text/html; charset=utf-8",
    "Cache-Control": "no-store",
  });
  response.end(html);
}

function escapeHtml(value) {
  return String(value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function escapeAttribute(value) {
  return escapeHtml(value).replace(/"/g, "&quot;");
}

server.listen(port, host, () => {
  console.log(`Open http://${host}:${port}/`);
});
