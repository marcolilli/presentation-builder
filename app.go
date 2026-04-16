package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type Presentation struct {
	Name       string `json:"name"`
	DeckURL    string `json:"deckUrl"`
	NotesURL   string `json:"notesUrl"`
	SourcePath string `json:"sourcePath"`
	BuiltAt    string `json:"builtAt"`
	CanRebuild bool   `json:"canRebuild"`
}

type SearchResult struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type BootState struct {
	BaseURL       string         `json:"baseUrl"`
	Presentations []Presentation `json:"presentations"`
}

type AppSettings struct {
	MarkdownRoots   []string `json:"markdownRoots"`
	ExportDirectory string   `json:"exportDirectory"`
}

type presentationMetadata struct {
	Name             string `json:"name"`
	SourcePath       string `json:"sourcePath"`
	PresentationPath string `json:"presentationPath"`
	NotesPath        string `json:"notesPath"`
	BuiltAt          string `json:"builtAt"`
}

type watchState struct {
	name           string
	sourcePath     string
	lastKnownMTime time.Time
	isBuilding     bool
	pending        bool
}

type App struct {
	ctx              context.Context
	rootDir          string
	presentationsDir string
	settingsPath     string
	baseURL          string
	httpServer       *http.Server
	httpListener     net.Listener
	watchTicker      *time.Ticker
	watchStop        chan struct{}
	watchMu          sync.Mutex
	watches          map[string]*watchState
	searchMu         sync.Mutex
	searchCache      markdownSearchCache
	eventMu          sync.Mutex
	eventClients     map[string]map[chan struct{}]struct{}
}

type markdownSearchCache struct {
	files    []searchFile
	key      string
	loadedAt time.Time
}

type searchFile struct {
	Name      string
	Path      string
	LowerName string
	LowerPath string
}

func NewApp() *App {
	rootDir := resolveAppRootDir()
	return &App{
		rootDir:          rootDir,
		presentationsDir: filepath.Join(rootDir, "presentations"),
		settingsPath:     filepath.Join(rootDir, "settings.json"),
		watchStop:        make(chan struct{}),
		watches:          make(map[string]*watchState),
		eventClients:     make(map[string]map[chan struct{}]struct{}),
	}
}

func resolveAppRootDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	executablePath, err := os.Executable()
	if err != nil {
		return cwd
	}

	cleanExecutablePath := filepath.Clean(executablePath)
	if !strings.Contains(cleanExecutablePath, ".app/Contents/MacOS/") {
		return cwd
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return cwd
	}

	return filepath.Join(homeDir, "Library", "Application Support", "Presentation Builder")
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	if err := a.startPresentationServer(); err != nil {
		wruntime.LogErrorf(ctx, "could not start presentation server: %v", err)
		return
	}

	a.restoreWatchers()
	a.startWatchLoop()
}

func (a *App) shutdown(ctx context.Context) {
	if a.watchTicker != nil {
		a.watchTicker.Stop()
	}

	select {
	case <-a.watchStop:
	default:
		close(a.watchStop)
	}

	if a.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.httpServer.Shutdown(shutdownCtx)
	}

	if a.httpListener != nil {
		_ = a.httpListener.Close()
	}

	a.eventMu.Lock()
	for _, clients := range a.eventClients {
		for client := range clients {
			close(client)
		}
	}
	a.eventClients = map[string]map[chan struct{}]struct{}{}
	a.eventMu.Unlock()
}

func (a *App) Boot() BootState {
	return BootState{
		BaseURL:       a.baseURL,
		Presentations: a.ListPresentations(),
	}
}

func (a *App) ListPresentations() []Presentation {
	entries, err := os.ReadDir(a.presentationsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Presentation{}
		}
		return []Presentation{}
	}

	presentations := make([]Presentation, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		dir := filepath.Join(a.presentationsDir, name)
		htmlPath := filepath.Join(dir, name+".html")
		notesPath := filepath.Join(dir, name+".notes.html")
		metadataPath := filepath.Join(dir, "presentation.json")

		if !fileExists(htmlPath) || !fileExists(notesPath) {
			continue
		}

		metadata := a.readMetadata(metadataPath)
		presentations = append(presentations, Presentation{
			Name:       name,
			DeckURL:    a.presentationURL(name, false),
			NotesURL:   a.presentationURL(name, true),
			SourcePath: metadata.SourcePath,
			BuiltAt:    metadata.BuiltAt,
			CanRebuild: metadata.SourcePath != "",
		})
	}

	sort.Slice(presentations, func(i, j int) bool {
		left := parseTime(presentations[i].BuiltAt)
		right := parseTime(presentations[j].BuiltAt)
		if !left.Equal(right) {
			return right.Before(left)
		}
		return strings.ToLower(presentations[i].Name) < strings.ToLower(presentations[j].Name)
	})

	return presentations
}

func (a *App) SearchMarkdownFiles(query string) []SearchResult {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return []SearchResult{}
	}

	files := a.getMarkdownSearchFiles()
	type scoredResult struct {
		file  searchFile
		score int
	}

	matches := make([]scoredResult, 0, len(files))
	for _, file := range files {
		score := scoreTieredMatch(normalized, file.LowerName, file.LowerPath)
		if score == minScore {
			continue
		}
		matches = append(matches, scoredResult{file: file, score: score})
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].file.Name < matches[j].file.Name
	})

	limit := 24
	if len(matches) < limit {
		limit = len(matches)
	}

	results := make([]SearchResult, 0, limit)
	for _, match := range matches[:limit] {
		results = append(results, SearchResult{
			Name: match.file.Name,
			Path: match.file.Path,
		})
	}
	return results
}

func (a *App) GetSettings() AppSettings {
	return a.readSettings()
}

func (a *App) SaveSettings(settings AppSettings) (AppSettings, error) {
	normalizedRoots := make([]string, 0, len(settings.MarkdownRoots))
	seen := make(map[string]struct{})

	for _, root := range settings.MarkdownRoots {
		trimmed := strings.TrimSpace(root)
		if trimmed == "" {
			continue
		}

		absoluteRoot, err := filepath.Abs(trimmed)
		if err != nil {
			return AppSettings{}, err
		}

		info, err := os.Stat(absoluteRoot)
		if err != nil {
			return AppSettings{}, fmt.Errorf("could not access %s", absoluteRoot)
		}
		if !info.IsDir() {
			return AppSettings{}, fmt.Errorf("%s is not a directory", absoluteRoot)
		}
		if _, ok := seen[absoluteRoot]; ok {
			continue
		}

		seen[absoluteRoot] = struct{}{}
		normalizedRoots = append(normalizedRoots, absoluteRoot)
	}

	sort.Strings(normalizedRoots)
	nextSettings := a.readSettings()
	nextSettings.MarkdownRoots = normalizedRoots
	if err := a.writeSettings(nextSettings); err != nil {
		return AppSettings{}, err
	}

	a.invalidateSearchCache()
	return nextSettings, nil
}

func (a *App) ChooseMarkdownRootDirectory() (string, error) {
	if a.ctx == nil {
		return "", errors.New("application context is not ready")
	}

	selectedPath, err := wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Choose Markdown Directory",
	})
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(selectedPath), nil
}

func (a *App) OpenSettings() error {
	if a.ctx == nil {
		return errors.New("application context is not ready")
	}

	wruntime.EventsEmit(a.ctx, "app:open-settings")
	return nil
}

func (a *App) BuildPresentation(sourcePath string) (BootState, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return BootState{}, errors.New("source path is required")
	}

	if err := a.runBuilder(sourcePath); err != nil {
		return BootState{}, err
	}

	name := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	a.ensureWatcher(name, sourcePath)
	a.invalidateSearchCache()
	return a.Boot(), nil
}

func (a *App) RebuildPresentation(name string) (BootState, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return BootState{}, errors.New("presentation name is required")
	}

	metadataPath := filepath.Join(a.presentationsDir, name, "presentation.json")
	metadata := a.readMetadata(metadataPath)
	if metadata.SourcePath == "" {
		return BootState{}, fmt.Errorf("no presentation metadata found for %s", name)
	}

	if err := a.runBuilder(metadata.SourcePath); err != nil {
		return BootState{}, err
	}

	a.ensureWatcher(name, metadata.SourcePath)
	return a.Boot(), nil
}

func (a *App) DeletePresentation(name string) (BootState, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return BootState{}, errors.New("presentation name is required")
	}

	targetDir := filepath.Clean(filepath.Join(a.presentationsDir, name))
	if !strings.HasPrefix(targetDir, a.presentationsDir+string(os.PathSeparator)) {
		return BootState{}, errors.New("invalid presentation path")
	}

	a.stopWatcher(name)
	if err := os.RemoveAll(targetDir); err != nil {
		return BootState{}, err
	}

	a.invalidateSearchCache()
	return a.Boot(), nil
}

func (a *App) OpenPresentation(name string, notes bool) error {
	if a.ctx == nil {
		return errors.New("application context is not ready")
	}

	wruntime.BrowserOpenURL(a.ctx, a.presentationURL(name, notes))
	return nil
}

func (a *App) OpenPresentationFolder(name string) error {
	targetDir := filepath.Join(a.presentationsDir, strings.TrimSpace(name))
	if !fileExists(targetDir) {
		return fmt.Errorf("presentation %s does not exist", name)
	}

	wruntime.BrowserOpenURL(a.ctx, "file://"+targetDir)
	return nil
}

func (a *App) ExportPresentationPDF(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("presentation name is required")
	}
	if a.ctx == nil {
		return "", errors.New("application context is not ready")
	}

	presentationDir := filepath.Join(a.presentationsDir, name)
	presentationPath := filepath.Join(presentationDir, name+".html")
	if !fileExists(presentationPath) {
		return "", fmt.Errorf("presentation %s does not exist", name)
	}

	lastExportDir := strings.TrimSpace(a.readSettings().ExportDirectory)
	if lastExportDir == "" {
		lastExportDir = a.defaultExportDirectory()
	}

	savePath, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:            "Export presentation as PDF",
		DefaultDirectory: lastExportDir,
		DefaultFilename:  name + ".pdf",
		Filters: []wruntime.FileFilter{
			{DisplayName: "PDF Files (*.pdf)", Pattern: "*.pdf"},
		},
	})
	if err != nil {
		return "", err
	}
	savePath = strings.TrimSpace(savePath)
	if savePath == "" {
		return "", nil
	}

	if err := exportPresentationPDF(a.presentationURL(name, false), savePath); err != nil {
		return "", err
	}
	a.rememberExportDirectory(filepath.Dir(savePath))

	return savePath, nil
}

func (a *App) startPresentationServer() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/events", a.handlePresentationEvents)
	mux.HandleFunc("/presentations/", a.handlePresentationAsset)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	a.httpListener = listener
	a.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}
	a.baseURL = "http://" + listener.Addr().String()

	go func() {
		if err := a.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			wruntime.LogErrorf(a.ctx, "presentation server stopped: %v", err)
		}
	}()

	return nil
}

func (a *App) handlePresentationAsset(writer http.ResponseWriter, request *http.Request) {
	relativePath := strings.TrimPrefix(request.URL.Path, "/")
	fullPath := filepath.Clean(filepath.Join(a.rootDir, filepath.FromSlash(relativePath)))
	if !strings.HasPrefix(fullPath, a.presentationsDir+string(os.PathSeparator)) {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		http.NotFound(writer, request)
		return
	}

	if info.IsDir() {
		fullPath = filepath.Join(fullPath, "index.html")
		info, err = os.Stat(fullPath)
		if err != nil {
			http.NotFound(writer, request)
			return
		}
	}

	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(fullPath)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", contentType)
	http.ServeFile(writer, request, fullPath)
}

func (a *App) runBuilder(sourcePath string) error {
	if !fileExists(sourcePath) {
		return fmt.Errorf("input file not found: %s", sourcePath)
	}
	_, err := buildPresentationFile(a.rootDir, sourcePath)
	return err
}

func (a *App) restoreWatchers() {
	for _, presentation := range a.ListPresentations() {
		if !presentation.CanRebuild || presentation.SourcePath == "" || !fileExists(presentation.SourcePath) {
			continue
		}

		if shouldRebuildPresentationOnStartup(presentation, a.presentationsDir) {
			_ = a.runBuilder(presentation.SourcePath)
		}

		a.ensureWatcher(presentation.Name, presentation.SourcePath)
	}
}

func (a *App) startWatchLoop() {
	a.watchTicker = time.NewTicker(1 * time.Second)

	go func() {
		for {
			select {
			case <-a.watchStop:
				return
			case <-a.watchTicker.C:
				a.pollWatchers()
			}
		}
	}()
}

func (a *App) pollWatchers() {
	a.watchMu.Lock()
	states := make([]*watchState, 0, len(a.watches))
	for _, state := range a.watches {
		states = append(states, state)
	}
	a.watchMu.Unlock()

	for _, state := range states {
		info, err := os.Stat(state.sourcePath)
		if err != nil {
			continue
		}

		modifiedAt := info.ModTime()
		if !modifiedAt.After(state.lastKnownMTime) {
			continue
		}

		state.lastKnownMTime = modifiedAt
		a.runWatchedBuild(state)
	}
}

func (a *App) ensureWatcher(name string, sourcePath string) {
	if !fileExists(sourcePath) {
		a.stopWatcher(name)
		return
	}

	info, err := os.Stat(sourcePath)
	if err != nil {
		return
	}

	a.watchMu.Lock()
	defer a.watchMu.Unlock()

	state, ok := a.watches[name]
	if ok && state.sourcePath == sourcePath {
		return
	}

	a.watches[name] = &watchState{
		name:           name,
		sourcePath:     sourcePath,
		lastKnownMTime: info.ModTime(),
	}
}

func (a *App) stopWatcher(name string) {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	delete(a.watches, name)
}

func (a *App) runWatchedBuild(state *watchState) {
	a.watchMu.Lock()
	if state.isBuilding {
		state.pending = true
		a.watchMu.Unlock()
		return
	}
	state.isBuilding = true
	a.watchMu.Unlock()

	go func() {
		defer func() {
			a.watchMu.Lock()
			state.isBuilding = false
			shouldRunAgain := state.pending
			state.pending = false
			a.watchMu.Unlock()

			if shouldRunAgain {
				a.runWatchedBuild(state)
				return
			}

			a.notifyPresentationReload(state.name)
			wruntime.EventsEmit(a.ctx, "presentations:changed", a.Boot())
		}()

		if err := a.runBuilder(state.sourcePath); err != nil {
			wruntime.LogErrorf(a.ctx, "watched build failed for %s: %v", state.name, err)
		}
	}()
}

func (a *App) readMetadata(metadataPath string) presentationMetadata {
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return presentationMetadata{}
	}

	var metadata presentationMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return presentationMetadata{}
	}
	return metadata
}

func (a *App) readSettings() AppSettings {
	data, err := os.ReadFile(a.settingsPath)
	if err != nil {
		return AppSettings{MarkdownRoots: []string{}, ExportDirectory: a.defaultExportDirectory()}
	}

	var settings AppSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return AppSettings{MarkdownRoots: []string{}, ExportDirectory: a.defaultExportDirectory()}
	}
	if settings.MarkdownRoots == nil {
		settings.MarkdownRoots = []string{}
	}
	if strings.TrimSpace(settings.ExportDirectory) == "" {
		settings.ExportDirectory = a.defaultExportDirectory()
	}
	return settings
}

func (a *App) defaultExportDirectory() string {
	if homeDir, err := os.UserHomeDir(); err == nil {
		downloadsDir := filepath.Join(homeDir, "Downloads")
		if info, err := os.Stat(downloadsDir); err == nil && info.IsDir() {
			return downloadsDir
		}
		if info, err := os.Stat(homeDir); err == nil && info.IsDir() {
			return homeDir
		}
	}
	return a.rootDir
}

func (a *App) rememberExportDirectory(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}

	nextSettings := a.readSettings()
	nextSettings.ExportDirectory = dir
	_ = a.writeSettings(nextSettings)
}

func (a *App) writeSettings(settings AppSettings) error {
	if err := os.MkdirAll(a.rootDir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.settingsPath, data, 0o644)
}

func (a *App) handlePresentationEvents(writer http.ResponseWriter, request *http.Request) {
	name := strings.TrimSpace(request.URL.Query().Get("name"))
	if name == "" {
		http.Error(writer, "missing presentation name", http.StatusBadRequest)
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Connection", "keep-alive")

	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming not supported", http.StatusInternalServerError)
		return
	}

	signal := make(chan struct{}, 1)
	a.addEventClient(name, signal)
	defer a.removeEventClient(name, signal)

	_, _ = fmt.Fprintf(writer, "event: connected\ndata: %s\n\n", mustMarshalJSONString(name))
	flusher.Flush()

	for {
		select {
		case <-request.Context().Done():
			return
		case <-signal:
			_, _ = writer.Write([]byte("event: reload\ndata: {}\n\n"))
			flusher.Flush()
		}
	}
}

func (a *App) addEventClient(name string, signal chan struct{}) {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	if a.eventClients[name] == nil {
		a.eventClients[name] = make(map[chan struct{}]struct{})
	}
	a.eventClients[name][signal] = struct{}{}
}

func (a *App) removeEventClient(name string, signal chan struct{}) {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	clients := a.eventClients[name]
	if clients == nil {
		return
	}
	delete(clients, signal)
	if len(clients) == 0 {
		delete(a.eventClients, name)
	}
}

func (a *App) notifyPresentationReload(name string) {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	for client := range a.eventClients[name] {
		select {
		case client <- struct{}{}:
		default:
		}
	}
}

func (a *App) presentationURL(name string, notes bool) string {
	safeName := urlPathEscape(name)
	suffix := ".html"
	if notes {
		suffix = ".notes.html"
	}
	return fmt.Sprintf("%s/presentations/%s/%s%s", a.baseURL, safeName, safeName, suffix)
}

func (a *App) invalidateSearchCache() {
	a.searchMu.Lock()
	defer a.searchMu.Unlock()
	a.searchCache = markdownSearchCache{}
}

func (a *App) getMarkdownSearchFiles() []searchFile {
	searchRoots := a.getMarkdownSearchRoots()
	cacheKey := strings.Join(searchRoots, "::")

	a.searchMu.Lock()
	defer a.searchMu.Unlock()

	if a.searchCache.key == cacheKey && time.Since(a.searchCache.loadedAt) < 15*time.Second {
		return a.searchCache.files
	}

	files := make([]searchFile, 0)
	seen := make(map[string]struct{})
	for _, root := range searchRoots {
		collectMarkdownFiles(root, &files, seen)
	}

	a.searchCache = markdownSearchCache{
		files:    files,
		key:      cacheKey,
		loadedAt: time.Now(),
	}
	return files
}

func (a *App) getMarkdownSearchRoots() []string {
	roots := make(map[string]struct{})
	settings := a.readSettings()
	for _, root := range settings.MarkdownRoots {
		if fileExists(root) {
			roots[root] = struct{}{}
		}
	}

	for _, presentation := range a.ListPresentations() {
		if presentation.SourcePath == "" || !fileExists(presentation.SourcePath) {
			continue
		}
		sourceDir := filepath.Dir(presentation.SourcePath)
		root := findSearchRoot(sourceDir)
		if root == "" {
			root = sourceDir
		}
		roots[root] = struct{}{}
	}

	if len(roots) == 0 {
		roots[a.rootDir] = struct{}{}
	}

	results := make([]string, 0, len(roots))
	for root := range roots {
		results = append(results, root)
	}
	sort.Strings(results)
	return results
}

func collectMarkdownFiles(directory string, results *[]searchFile, seen map[string]struct{}) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}

		fullPath := filepath.Join(directory, name)
		if entry.IsDir() {
			if name == "node_modules" || name == "presentations" {
				continue
			}
			collectMarkdownFiles(fullPath, results, seen)
			continue
		}

		if filepath.Ext(name) != ".md" {
			continue
		}
		if _, ok := seen[fullPath]; ok {
			continue
		}

		seen[fullPath] = struct{}{}
		*results = append(*results, searchFile{
			Name:      name,
			Path:      fullPath,
			LowerName: strings.ToLower(name),
			LowerPath: strings.ToLower(fullPath),
		})
	}
}

func shouldRebuildPresentationOnStartup(presentation Presentation, presentationsDir string) bool {
	if presentation.SourcePath == "" || !fileExists(presentation.SourcePath) {
		return false
	}

	sourceMTime := fileModTime(presentation.SourcePath)
	htmlPath := filepath.Join(presentationsDir, presentation.Name, presentation.Name+".html")
	notesPath := filepath.Join(presentationsDir, presentation.Name, presentation.Name+".notes.html")
	latestGenerated := maxTime(fileModTime(htmlPath), fileModTime(notesPath), parseTime(presentation.BuiltAt))
	return sourceMTime.After(latestGenerated)
}

func fileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func maxTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value
		}
	}
	return latest
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func findSearchRoot(startDir string) string {
	currentDir := startDir
	for {
		if fileExists(filepath.Join(currentDir, ".obsidian")) {
			return currentDir
		}
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return ""
		}
		currentDir = parentDir
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

const minScore = -1 << 30

func scoreTieredMatch(query string, primaryValue string, secondaryValue string) int {
	normalizedQuery := strings.TrimSpace(strings.ToLower(query))
	if normalizedQuery == "" {
		return 0
	}

	compactQuery := strings.ReplaceAll(normalizedQuery, " ", "")
	primary := strings.ToLower(primaryValue)
	secondary := strings.ToLower(secondaryValue)
	primaryTokens := tokenizeSearchText(primary)
	primaryAcronym := buildSearchAcronym(primary)
	compactPrimary := strings.ReplaceAll(primary, " ", "")
	compactSecondary := strings.ReplaceAll(secondary, " ", "")

	if primary == normalizedQuery {
		return 10000
	}
	if strings.HasPrefix(primary, normalizedQuery) {
		return 9000 - len(primary)
	}
	for index, token := range primaryTokens {
		if strings.HasPrefix(token, normalizedQuery) {
			return 8000 - index*20 - len(token)
		}
	}
	if position := strings.Index(primary, normalizedQuery); position >= 0 {
		return 7000 - position
	}
	if primaryAcronym != "" && strings.HasPrefix(primaryAcronym, compactQuery) {
		return 6000 - len(primaryAcronym)
	}

	primaryFuzzyScore := scoreFuzzySubsequence(compactQuery, compactPrimary)
	if primaryFuzzyScore >= 24 {
		return 5000 + primaryFuzzyScore
	}

	secondaryTokens := tokenizeSearchText(secondary)
	for index, token := range secondaryTokens {
		if strings.HasPrefix(token, normalizedQuery) {
			return 2000 - index*5 - len(token)
		}
	}
	if position := strings.Index(secondary, normalizedQuery); position >= 0 {
		return 1500 - position
	}

	secondaryFuzzyScore := scoreFuzzySubsequence(compactQuery, compactSecondary)
	if secondaryFuzzyScore >= 30 {
		return 1000 + secondaryFuzzyScore
	}

	return minScore
}

func scoreFuzzySubsequence(query string, target string) int {
	if query == "" {
		return minScore
	}

	targetIndex := 0
	score := 0
	streak := 0
	firstMatchIndex := -1

	for _, character := range query {
		foundIndex := strings.IndexRune(target[targetIndex:], character)
		if foundIndex == -1 {
			return minScore
		}
		foundIndex += targetIndex

		if firstMatchIndex == -1 {
			firstMatchIndex = foundIndex
		}
		if foundIndex == targetIndex {
			streak++
			score += 8 + streak
		} else {
			streak = 0
			gap := foundIndex - targetIndex
			if gap > 2 {
				return minScore
			}
			score += maxInt(1, 4-gap)
		}

		targetIndex = foundIndex + 1
	}

	if firstMatchIndex > 6 {
		score -= firstMatchIndex * 2
	}
	return score
}

func tokenizeSearchText(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	})
	results := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			results = append(results, field)
		}
	}
	return results
}

func buildSearchAcronym(value string) string {
	tokens := tokenizeSearchText(value)
	var builder strings.Builder
	for _, token := range tokens {
		builder.WriteByte(token[0])
	}
	return builder.String()
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func urlPathEscape(value string) string {
	return url.PathEscape(value)
}
