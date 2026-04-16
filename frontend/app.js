import {
  Boot,
  BuildPresentation,
  ChooseMarkdownRootDirectory,
  DeletePresentation,
  ExportPresentationPDF,
  GetSettings,
  OpenPresentation,
  RebuildPresentation,
  SaveSettings,
  SearchMarkdownFiles,
} from "./wailsjs/go/main/App.js";

const addModalButton = document.getElementById("open-add-modal");
const addDialog = document.getElementById("add-presentation-dialog");
const closeAddModalButton = document.getElementById("close-add-modal");
const cancelAddModalButton = document.getElementById("cancel-add-modal");
const addForm = document.getElementById("add-presentation-form");
const searchInput = document.getElementById("source-search");
const searchResults = document.getElementById("search-results");
const searchStatus = document.getElementById("search-status");
const selectedSource = document.getElementById("selected-source");
const submitAddPresentationButton = document.getElementById("submit-add-presentation");
const toggleSettingsPageButton = document.getElementById("toggle-settings-page");
const presentationsPage = document.getElementById("presentations-page");
const settingsPage = document.getElementById("settings-page");
const presentationFilterInput = document.getElementById("presentation-filter");
const presentationList = document.getElementById("presentation-list");
const toast = document.getElementById("toast");
const toastMessage = document.getElementById("toast-message");
const toastSpinner = document.getElementById("toast-spinner");
const addMarkdownRootButton = document.getElementById("add-markdown-root");
const markdownRootsList = document.getElementById("markdown-roots-list");
const markdownRootsEmpty = document.getElementById("markdown-roots-empty");

let selectedSourcePath = "";
let currentPresentations = [];
let currentSettings = { markdownRoots: [] };
let searchRequestId = 0;
let searchDebounceTimer = null;
let currentPage = "presentations";
let toastHideTimer = null;

function clearToast() {
  toast.hidden = true;
  toastMessage.textContent = "";
  toastSpinner.hidden = true;
}

function setMessage(text, options = {}) {
  const value = String(text || "").trim();
  clearTimeout(toastHideTimer);
  toastHideTimer = null;

  if (!value) {
    clearToast();
    return;
  }

  toast.hidden = false;
  toastMessage.textContent = value;
  toastSpinner.hidden = !options.loading;

  if (!options.loading && !options.persistent) {
    toastHideTimer = setTimeout(() => {
      clearToast();
      toastHideTimer = null;
    }, 2500);
  }
}

function updateNavigationState() {
  const showingPresentations = currentPage === "presentations";
  presentationsPage.hidden = !showingPresentations;
  settingsPage.hidden = showingPresentations;
  toggleSettingsPageButton.setAttribute("aria-label", showingPresentations ? "Open settings" : "Close settings");
  toggleSettingsPageButton.classList.toggle("is-settings-open", !showingPresentations);
}

function showPage(page) {
  currentPage = page === "settings" ? "settings" : "presentations";
  updateNavigationState();

  if (currentPage === "presentations") {
    presentationFilterInput.focus();
    return;
  }

  addMarkdownRootButton.focus();
}

function openSettingsPage() {
  showPage("settings");
}

function escapeHtmlForHtml(value) {
  return String(value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function escapeHtmlForAttribute(value) {
  return escapeHtmlForHtml(value).replace(/"/g, "&quot;");
}

function resetAddPresentationState() {
  searchInput.value = "";
  selectedSourcePath = "";
  submitAddPresentationButton.disabled = true;
  selectedSource.hidden = true;
  selectedSource.textContent = "";
  searchStatus.textContent = "Type to search for a source file.";
  renderSearchResults([]);
}

function openAddPresentationDialog() {
  resetAddPresentationState();
  if (typeof addDialog.showModal === "function") {
    addDialog.showModal();
  } else {
    addDialog.setAttribute("open", "open");
  }
  searchInput.focus();
}

function closeAddPresentationDialog() {
  if (typeof addDialog.close === "function") {
    addDialog.close();
  } else {
    addDialog.removeAttribute("open");
  }
}

function renderSearchResults(results) {
  if (!results.length) {
    searchResults.hidden = true;
    searchResults.innerHTML = "";
    return;
  }

  searchResults.hidden = false;
  searchResults.innerHTML = results
    .map(
      (result, index) => `
        <button type="button" class="search-result" data-index="${index}">
          <span class="search-result-name">${escapeHtmlForHtml(result.name)}</span>
          <span class="search-result-path">${escapeHtmlForHtml(result.path)}</span>
        </button>
      `,
    )
    .join("");

  searchResults.querySelectorAll(".search-result").forEach((button) => {
    button.addEventListener("click", () => {
      const index = Number(button.dataset.index || "-1");
      if (index < 0 || index >= results.length) {
        return;
      }
      chooseSearchResult(results[index].name, results[index].path);
    });
  });
}

function chooseSearchResult(name, sourcePath) {
  selectedSourcePath = sourcePath;
  submitAddPresentationButton.disabled = false;
  selectedSource.hidden = false;
  selectedSource.innerHTML =
    "<strong>Selected:</strong> " +
    escapeHtmlForHtml(name) +
    "<br />" +
    escapeHtmlForHtml(sourcePath);
  searchStatus.textContent = "Ready to add presentation.";
  searchInput.value = name;
  renderSearchResults([]);
}

async function performSearch(query) {
  const trimmedQuery = query.trim();
  searchRequestId += 1;
  const requestId = searchRequestId;

  if (!trimmedQuery) {
    selectedSourcePath = "";
    submitAddPresentationButton.disabled = true;
    selectedSource.hidden = true;
    selectedSource.textContent = "";
    searchStatus.textContent = "Type to search for a source file.";
    renderSearchResults([]);
    return;
  }

  selectedSourcePath = "";
  submitAddPresentationButton.disabled = true;
  selectedSource.hidden = true;
  selectedSource.textContent = "";
  searchStatus.textContent = "Searching...";

  try {
    const results = await SearchMarkdownFiles(trimmedQuery);
    if (requestId !== searchRequestId) {
      return;
    }

    renderSearchResults(Array.isArray(results) ? results : []);
    searchStatus.textContent = results.length
      ? "Choose a source file from the list."
      : "No matching Markdown files found.";
  } catch (error) {
    if (requestId !== searchRequestId) {
      return;
    }

    renderSearchResults([]);
    searchStatus.textContent = error.message || "Could not search files right now.";
  }
}

function formatBuiltAt(value) {
  if (!value) {
    return "Build time unknown";
  }

  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    return value;
  }

  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(parsed);
}

function closePresentationMenus() {
  Array.from(document.querySelectorAll(".presentation-actions")).forEach((menu) => {
    menu.removeAttribute("open");
  });
}

function matchesPresentationFilter(query, presentation) {
  if (!query.trim()) {
    return true;
  }

  const text = `${presentation.name} ${presentation.sourcePath || ""}`.toLowerCase();
  return text.includes(query.trim().toLowerCase());
}

function renderPresentations(presentations) {
  const query = presentationFilterInput.value || "";
  const filtered = presentations.filter((presentation) => matchesPresentationFilter(query, presentation));

  if (!filtered.length) {
    presentationList.innerHTML = `<p class="empty">No generated presentations found yet.</p>`;
    return;
  }

  presentationList.innerHTML = filtered
    .map(
      (presentation, index) => `
        <section
          class="presentation-card"
          data-presentation-name="${escapeHtmlForAttribute(presentation.name.toLowerCase())}"
          data-presentation-source="${escapeHtmlForAttribute((presentation.sourcePath || "").toLowerCase())}"
        >
          <details class="presentation-actions">
            <summary aria-label="Presentation options">⋯</summary>
            <div class="presentation-menu">
              ${
                presentation.canRebuild
                  ? `<button class="secondary-button" type="button" data-action="rebuild" data-index="${index}">Rebuild</button>`
                  : ""
              }
              <button class="secondary-button" type="button" data-action="export-pdf" data-index="${index}">Download PDF</button>
              <button class="secondary-button" type="button" data-action="delete" data-index="${index}">Delete</button>
            </div>
          </details>
          <h3>${escapeHtmlForHtml(presentation.name)}</h3>
          <div class="presentation-links">
            <button class="secondary-action" type="button" data-action="slides" data-index="${index}">Open slides</button>
            <button class="secondary-action" type="button" data-action="notes" data-index="${index}">Open notes</button>
          </div>
          <div class="presentation-meta">
            ${presentation.builtAt ? `Last built: ${escapeHtmlForHtml(formatBuiltAt(presentation.builtAt))}` : "Build time unknown"}
          </div>
          <div class="presentation-meta">
            ${presentation.sourcePath ? `Source: ${escapeHtmlForHtml(presentation.sourcePath)}` : "Source unknown"}
          </div>
        </section>
      `,
    )
    .join("");

  presentationList.querySelectorAll("[data-action]").forEach((button) => {
    button.addEventListener("click", async () => {
      const action = button.dataset.action;
      const index = Number(button.dataset.index || "-1");
      if (index < 0 || index >= filtered.length) {
        return;
      }

      const presentation = filtered[index];
      try {
        if (action === "slides") {
          await OpenPresentation(presentation.name, false);
          return;
        }
        if (action === "notes") {
          await OpenPresentation(presentation.name, true);
          return;
        }
        if (action === "rebuild") {
          setMessage(`Rebuilding ${presentation.name}...`, { loading: true });
          const state = await RebuildPresentation(presentation.name);
          await syncFromState(state);
          closePresentationMenus();
          setMessage(`Rebuilt ${presentation.name}.`);
          return;
        }
        if (action === "export-pdf") {
          setMessage(`Exporting ${presentation.name} to PDF...`, { loading: true });
          const savedPath = await ExportPresentationPDF(presentation.name);
          closePresentationMenus();
          if (savedPath) {
            setMessage(`Saved PDF to ${savedPath}.`);
          } else {
            setMessage("");
          }
          return;
        }
        if (action === "delete") {
          setMessage(`Deleting ${presentation.name}...`, { loading: true });
          const state = await DeletePresentation(presentation.name);
          await syncFromState(state);
          setMessage(`Deleted ${presentation.name}.`);
          closePresentationMenus();
        }
      } catch (error) {
        setMessage(error.message || String(error));
      }
    });
  });
}

function renderMarkdownRoots() {
  const roots = Array.isArray(currentSettings.markdownRoots) ? currentSettings.markdownRoots : [];
  markdownRootsEmpty.hidden = roots.length > 0;

  if (!roots.length) {
    markdownRootsList.innerHTML = "";
    return;
  }

  markdownRootsList.innerHTML = roots
    .map(
      (rootPath, index) => `
        <div class="settings-list-item">
          <div class="settings-list-copy">
            <div class="settings-list-title">${escapeHtmlForHtml(rootPath)}</div>
          </div>
          <button type="button" class="settings-remove-button" data-index="${index}" aria-label="Remove ${escapeHtmlForAttribute(rootPath)}">x</button>
        </div>
      `,
    )
    .join("");

  markdownRootsList.querySelectorAll(".settings-remove-button").forEach((button) => {
    button.addEventListener("click", async () => {
      const index = Number(button.dataset.index || "-1");
      if (index < 0 || index >= currentSettings.markdownRoots.length) {
        return;
      }

      const nextRoots = currentSettings.markdownRoots.filter((_, currentIndex) => currentIndex !== index);
      try {
        currentSettings = await SaveSettings({ markdownRoots: nextRoots });
        renderMarkdownRoots();
      } catch (error) {
        setMessage(error.message || String(error));
      }
    });
  });
}

async function syncFromState(state) {
  currentPresentations = Array.isArray(state.presentations) ? state.presentations : [];
  renderPresentations(currentPresentations);
}

addModalButton.addEventListener("click", openAddPresentationDialog);
closeAddModalButton.addEventListener("click", closeAddPresentationDialog);
cancelAddModalButton.addEventListener("click", closeAddPresentationDialog);
toggleSettingsPageButton.addEventListener("click", () => {
  if (currentPage === "settings") {
    showPage("presentations");
    return;
  }

  openSettingsPage();
});

addDialog.addEventListener("click", (event) => {
  if (event.target === addDialog) {
    closeAddPresentationDialog();
  }
});

searchInput.addEventListener("input", () => {
  clearTimeout(searchDebounceTimer);
  searchDebounceTimer = setTimeout(() => {
    performSearch(searchInput.value);
  }, 120);
});

addForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!selectedSourcePath) {
    searchStatus.textContent = "Select a source file before adding.";
    return;
  }

  try {
    setMessage("Adding presentation...", { loading: true });
    const state = await BuildPresentation(selectedSourcePath);
    await syncFromState(state);
    closeAddPresentationDialog();
    setMessage("Presentation added.");
  } catch (error) {
    setMessage(error.message || String(error));
  }
});

presentationFilterInput.addEventListener("input", () => {
  renderPresentations(currentPresentations);
});

addMarkdownRootButton.addEventListener("click", async () => {
  try {
    setMessage("Updating settings...", { loading: true });
    const selectedPath = await ChooseMarkdownRootDirectory();
    if (!selectedPath) {
      setMessage("");
      return;
    }

    const nextRoots = [...(currentSettings.markdownRoots || [])];
    if (!nextRoots.includes(selectedPath)) {
      nextRoots.push(selectedPath);
    }

    currentSettings = await SaveSettings({ markdownRoots: nextRoots });
    renderMarkdownRoots();
    setMessage("Settings updated.");
  } catch (error) {
    setMessage(error.message || String(error));
  }
});

document.addEventListener("click", (event) => {
  if (event.target.closest(".presentation-actions")) {
    return;
  }

  closePresentationMenus();
});

document.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key === ",") {
    event.preventDefault();
    openSettingsPage();
    return;
  }

  if (event.key === "Escape") {
    if (currentPage === "settings") {
      showPage("presentations");
      return;
    }

    closePresentationMenus();
  }
});

if (window.runtime?.EventsOn) {
  window.runtime.EventsOn("app:open-settings", () => {
    openSettingsPage();
  });

  window.runtime.EventsOn("presentations:changed", async (state) => {
    await syncFromState(state);
  });
}

try {
  updateNavigationState();
  const [state, settings] = await Promise.all([Boot(), GetSettings()]);
  currentSettings = settings || { markdownRoots: [] };
  renderMarkdownRoots();
  await syncFromState(state);
  presentationFilterInput.focus();
} catch (error) {
  setMessage(error.message || String(error));
}
