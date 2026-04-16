package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const cssPixelsPerInch = 96.0

type pdfDocument struct {
	Bytes []byte
}

type slideGeometry struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type cdpClient struct {
	conn         *websocket.Conn
	writeMu      sync.Mutex
	nextID       int64
	pendingMu    sync.Mutex
	pending      map[int64]chan cdpMessage
	eventMu      sync.Mutex
	eventWaiters map[string][]chan json.RawMessage
	closed       chan struct{}
}

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type pdfObject struct {
	oldID     int
	body      []byte
	isCatalog bool
	isPages   bool
	isPage    bool
}

type parsedPDFDocument struct {
	objects   []pdfObject
	objectMap map[int]int
}

var (
	pdfObjectPattern = regexp.MustCompile(`(?m)^(\d+)\s+(\d+)\s+obj\b`)
	pdfRefPattern    = regexp.MustCompile(`(\d+)\s+(\d+)\s+R`)
)

func exportPresentationPDF(presentationURL string, outputPath string) error {
	chromePath, err := findChromeBinary()
	if err != nil {
		return err
	}

	tempDir, err := os.MkdirTemp("", "presentation-builder-pdf-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	port, err := reservePort()
	if err != nil {
		return err
	}

	chromeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chromeCmd, err := startChromeHeadless(chromeCtx, chromePath, tempDir, port)
	if err != nil {
		return err
	}
	defer func() {
		cancel()
		_ = chromeCmd.Process.Kill()
		_ = chromeCmd.Wait()
	}()

	wsURL, err := waitForChromePageWebSocket(port)
	if err != nil {
		return err
	}

	client, err := newCDPClient(wsURL)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Call(context.Background(), "Page.enable", nil, nil); err != nil {
		return err
	}
	if err := client.Call(context.Background(), "Runtime.enable", nil, nil); err != nil {
		return err
	}
	if err := client.Call(context.Background(), "Emulation.setDeviceMetricsOverride", map[string]any{
		"width":             1123,
		"height":            800,
		"deviceScaleFactor": 1,
		"mobile":            false,
		"screenWidth":       1123,
		"screenHeight":      800,
		"fitWindow":         false,
		"screenOrientation": map[string]any{"type": "landscapePrimary", "angle": 0},
	}, nil); err != nil {
		return err
	}

	firstSlideURL := withQueryParams(presentationURL, map[string]string{
		"slide":     "1",
		"exportPdf": "1",
	})
	if err := navigateAndWait(context.Background(), client, firstSlideURL); err != nil {
		return err
	}

	slideCount, err := evaluateInt(context.Background(), client, "document.querySelectorAll('.slide').length")
	if err != nil {
		return err
	}
	if slideCount <= 1 {
		slideCount, err = evaluateInt(context.Background(), client, "window.__presentationSlideCount || document.querySelectorAll('.slide').length")
		if err != nil {
			return err
		}
	}
	if slideCount < 1 {
		return errors.New("no slides were rendered")
	}

	documents := make([]pdfDocument, 0, slideCount)
	for index := 1; index <= slideCount; index++ {
		slideURL := withQueryParams(presentationURL, map[string]string{
			"slide":     strconv.Itoa(index),
			"exportPdf": "1",
		})
		if err := navigateAndWait(context.Background(), client, slideURL); err != nil {
			return err
		}

		geometry, err := evaluateGeometry(context.Background(), client, "window.__presentationExportGeometry || null")
		if err != nil {
			return err
		}
		if geometry.Width <= 0 {
			geometry.Width = mmToPx(320)
		}
		if geometry.Height <= 0 {
			geometry.Height = mmToPx(200)
		}

		pdfBytes, err := renderSinglePagePDF(context.Background(), client, geometry)
		if err != nil {
			return err
		}

		documents = append(documents, pdfDocument{Bytes: pdfBytes})
	}

	return writeMergedPDF(outputPath, documents)
}

func findChromeBinary() (string, error) {
	candidates := []string{
		strings.TrimSpace(os.Getenv("CHROME_BIN")),
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if fileExists(candidate) {
			return candidate, nil
		}
	}

	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge", "msedge"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}

	return "", errors.New("could not find a Chrome or Chromium binary")
}

func reservePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func startChromeHeadless(ctx context.Context, chromePath string, profileDir string, port int) (*exec.Cmd, error) {
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		"--mute-audio",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-extensions",
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--user-data-dir=" + profileDir,
		"about:blank",
	}

	cmd := exec.CommandContext(ctx, chromePath, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func waitForChromePageWebSocket(port int) (string, error) {
	versionURL := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(versionURL)
		if err == nil {
			var targets []struct {
				Type                 string `json:"type"`
				URL                  string `json:"url"`
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			func() {
				defer response.Body.Close()
				_ = json.NewDecoder(response.Body).Decode(&targets)
			}()
			for _, target := range targets {
				if target.Type == "page" && target.WebSocketDebuggerURL != "" {
					return target.WebSocketDebuggerURL, nil
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}

	return "", errors.New("timed out waiting for Chrome debugging target")
}

func newCDPClient(wsURL string) (*cdpClient, error) {
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}

	client := &cdpClient{
		conn:         conn,
		pending:      make(map[int64]chan cdpMessage),
		eventWaiters: make(map[string][]chan json.RawMessage),
		closed:       make(chan struct{}),
	}

	go client.readLoop()
	return client, nil
}

func (c *cdpClient) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return c.conn.Close()
}

func (c *cdpClient) readLoop() {
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			c.signalClosed()
			return
		}

		var payload cdpMessage
		if err := json.Unmarshal(message, &payload); err != nil {
			continue
		}

		if payload.ID != 0 {
			c.pendingMu.Lock()
			ch := c.pending[payload.ID]
			delete(c.pending, payload.ID)
			c.pendingMu.Unlock()
			if ch != nil {
				ch <- payload
			}
			continue
		}

		if payload.Method != "" {
			c.eventMu.Lock()
			waiters := c.eventWaiters[payload.Method]
			delete(c.eventWaiters, payload.Method)
			c.eventMu.Unlock()
			for _, waiter := range waiters {
				waiter <- payload.Params
			}
		}
	}
}

func (c *cdpClient) signalClosed() {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
}

func (c *cdpClient) Call(ctx context.Context, method string, params any, result any) error {
	id := atomic.AddInt64(&c.nextID, 1)
	responseCh := make(chan cdpMessage, 1)

	c.pendingMu.Lock()
	c.pending[id] = responseCh
	c.pendingMu.Unlock()

	message := map[string]any{
		"id":     id,
		"method": method,
	}
	if params != nil {
		message["params"] = params
	}

	c.writeMu.Lock()
	err := c.conn.WriteJSON(message)
	c.writeMu.Unlock()
	if err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return errors.New("chrome connection closed")
	case response := <-responseCh:
		if response.Error != nil {
			return fmt.Errorf("cdp %s: %s", method, response.Error.Message)
		}
		if result == nil || len(response.Result) == 0 {
			return nil
		}
		return json.Unmarshal(response.Result, result)
	}
}

func (c *cdpClient) waitForEvent(ctx context.Context, method string) (json.RawMessage, error) {
	waiter := c.registerEventWaiter(method)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, errors.New("chrome connection closed")
	case payload := <-waiter:
		return payload, nil
	}
}

func (c *cdpClient) registerEventWaiter(method string) chan json.RawMessage {
	waiter := make(chan json.RawMessage, 1)
	c.eventMu.Lock()
	c.eventWaiters[method] = append(c.eventWaiters[method], waiter)
	c.eventMu.Unlock()
	return waiter
}

func navigateAndWait(ctx context.Context, client *cdpClient, targetURL string) error {
	loadEvent := client.registerEventWaiter("Page.loadEventFired")
	if err := client.Call(ctx, "Page.navigate", map[string]any{
		"url": targetURL,
	}, nil); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-client.closed:
		return errors.New("chrome connection closed")
	case <-loadEvent:
	}

	if err := client.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    "window.__presentationReady",
		"awaitPromise":  true,
		"returnByValue": true,
	}, nil); err != nil {
		return err
	}

	return nil
}

func evaluateInt(ctx context.Context, client *cdpClient, expression string) (int, error) {
	var response struct {
		Result struct {
			Type  string      `json:"type"`
			Value interface{} `json:"value"`
		} `json:"result"`
	}

	if err := client.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
	}, &response); err != nil {
		return 0, err
	}

	switch value := response.Result.Value.(type) {
	case float64:
		return int(value), nil
	case int:
		return value, nil
	default:
		return 0, fmt.Errorf("unexpected evaluation result for %s", expression)
	}
}

func evaluateGeometry(ctx context.Context, client *cdpClient, expression string) (slideGeometry, error) {
	var response struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}

	if err := client.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
	}, &response); err != nil {
		return slideGeometry{}, err
	}

	if len(bytes.TrimSpace(response.Result.Value)) == 0 || bytes.Equal(bytes.TrimSpace(response.Result.Value), []byte("null")) {
		return slideGeometry{}, nil
	}

	var geometry slideGeometry
	if err := json.Unmarshal(response.Result.Value, &geometry); err != nil {
		return slideGeometry{}, err
	}
	return geometry, nil
}

func printPDFWithSize(ctx context.Context, client *cdpClient, pageWidthInches float64, pageHeightInches float64) ([]byte, error) {
	var response struct {
		Data string `json:"data"`
	}

	if err := client.Call(ctx, "Page.printToPDF", map[string]any{
		"printBackground":     true,
		"landscape":           false,
		"displayHeaderFooter": false,
		"paperWidth":          pageWidthInches,
		"paperHeight":         pageHeightInches,
		"marginTop":           0,
		"marginBottom":        0,
		"marginLeft":          0,
		"marginRight":         0,
		"scale":               1,
		"preferCSSPageSize":   false,
	}, &response); err != nil {
		return nil, err
	}

	decoded, err := base64.StdEncoding.DecodeString(response.Data)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func renderSinglePagePDF(ctx context.Context, client *cdpClient, geometry slideGeometry) ([]byte, error) {
	pageWidthInches, pageHeightInches := geometryToPaperSize(geometry)
	if pageWidthInches <= 0 || pageHeightInches <= 0 {
		return nil, errors.New("invalid slide geometry")
	}

	pdfBytes, err := printPDFWithSize(ctx, client, pageWidthInches, pageHeightInches)
	if err != nil {
		return nil, err
	}

	pageCount, err := countPDFPages(pdfBytes)
	if err != nil {
		return nil, err
	}
	if pageCount <= 1 {
		return pdfBytes, nil
	}

	lowHeight := pageHeightInches
	highHeight := pageHeightInches
	highBytes := pdfBytes

	for attempts := 0; attempts < 8 && pageCount > 1; attempts++ {
		lowHeight = highHeight
		highHeight = math.Ceil(highHeight*1.5*100) / 100
		highBytes, err = printPDFWithSize(ctx, client, pageWidthInches, highHeight)
		if err != nil {
			return nil, err
		}
		pageCount, err = countPDFPages(highBytes)
		if err != nil {
			return nil, err
		}
	}

	if pageCount > 1 {
		return nil, fmt.Errorf("slide still spans %d pages after expanding height", pageCount)
	}

	bestBytes := highBytes
	for attempts := 0; attempts < 6; attempts++ {
		midHeight := math.Floor(((lowHeight+highHeight)/2)*100) / 100
		if midHeight <= lowHeight || midHeight >= highHeight {
			break
		}

		midBytes, err := printPDFWithSize(ctx, client, pageWidthInches, midHeight)
		if err != nil {
			return nil, err
		}
		midPages, err := countPDFPages(midBytes)
		if err != nil {
			return nil, err
		}
		if midPages <= 1 {
			highHeight = midHeight
			bestBytes = midBytes
			continue
		}
		lowHeight = midHeight
	}

	return bestBytes, nil
}

func countPDFPages(data []byte) (int, error) {
	objects, err := parsePDFObjects(data)
	if err != nil {
		return 0, err
	}

	pages := 0
	for _, object := range objects {
		if object.isPage {
			pages++
		}
	}
	if pages == 0 {
		return 0, errors.New("pdf contains no pages")
	}
	return pages, nil
}

func geometryToPaperSize(geometry slideGeometry) (float64, float64) {
	widthPx := geometry.Width
	if widthPx <= 0 {
		widthPx = mmToPx(320)
	}
	heightPx := geometry.Height
	if heightPx <= 0 {
		heightPx = mmToPx(200)
	}

	widthInches := float64(widthPx) / cssPixelsPerInch
	heightInches := float64(heightPx) / cssPixelsPerInch
	return widthInches, heightInches
}

func mmToPx(mm int) int {
	return int((float64(mm) * cssPixelsPerInch / 25.4) + 0.5)
}

func writeMergedPDF(outputPath string, documents []pdfDocument) error {
	if len(documents) == 0 {
		return errors.New("no pages to export")
	}

	type mergedObject struct {
		id   int
		body []byte
	}

	parsedDocuments := make([]parsedPDFDocument, 0, len(documents))
	nextObjectID := 3
	for sourceIndex, document := range documents {
		objects, err := parsePDFObjects(document.Bytes)
		if err != nil {
			return fmt.Errorf("pdf document %d: %w", sourceIndex+1, err)
		}

		var pagesID int
		var pageID int
		objectMap := map[int]int{
			0: 0,
		}
		for _, object := range objects {
			if object.isCatalog {
				objectMap[object.oldID] = 1
				continue
			}
			if object.isPages {
				pagesID = object.oldID
				objectMap[object.oldID] = 2
				continue
			}
			if object.isPage {
				pageID = object.oldID
			}
			objectMap[object.oldID] = nextObjectID
			nextObjectID++
		}

		if pagesID == 0 {
			return fmt.Errorf("pdf document %d missing pages tree", sourceIndex+1)
		}
		if pageID == 0 {
			return fmt.Errorf("pdf document %d missing page object", sourceIndex+1)
		}

		parsedDocuments = append(parsedDocuments, parsedPDFDocument{
			objects:   objects,
			objectMap: objectMap,
		})
	}

	mergedObjects := make([]mergedObject, 0, nextObjectID-3)
	pageRefs := make([]int, 0, len(parsedDocuments))
	for _, document := range parsedDocuments {
		for _, object := range document.objects {
			if object.isCatalog || object.isPages {
				continue
			}
			newID := document.objectMap[object.oldID]
			if newID == 0 {
				return fmt.Errorf("missing object mapping for %d", object.oldID)
			}
			rewritten := rewritePDFObjectBody(object.body, document.objectMap)
			mergedObjects = append(mergedObjects, mergedObject{
				id:   newID,
				body: rewritten,
			})
			if object.isPage {
				pageRefs = append(pageRefs, newID)
			}
		}
	}

	sort.Slice(mergedObjects, func(i, j int) bool {
		return mergedObjects[i].id < mergedObjects[j].id
	})
	sort.Ints(pageRefs)

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}

	var buffer bytes.Buffer
	buffer.WriteString("%PDF-1.4\n%\xFF\xFF\xFF\xFF\n")

	type objectSpec struct {
		id   int
		data []byte
	}

	objects := make([]objectSpec, 0, len(mergedObjects)+2)
	objects = append(objects, objectSpec{id: 1, data: []byte("<< /Type /Catalog /Pages 2 0 R >>")})

	kids := make([]string, 0, len(pageRefs))
	for _, pageRef := range pageRefs {
		kids = append(kids, fmt.Sprintf("%d 0 R", pageRef))
	}
	objects = append(objects, objectSpec{
		id:   2,
		data: []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pageRefs))),
	})
	for _, object := range mergedObjects {
		objects = append(objects, objectSpec{id: object.id, data: object.body})
	}

	offsets := make([]int, len(objects)+3)
	for _, object := range objects {
		offsets[object.id] = buffer.Len()
		buffer.WriteString(fmt.Sprintf("%d 0 obj\n", object.id))
		buffer.Write(object.data)
		if len(object.data) == 0 || object.data[len(object.data)-1] != '\n' {
			buffer.WriteByte('\n')
		}
		buffer.WriteString("endobj\n")
	}

	xrefStart := buffer.Len()
	buffer.WriteString(fmt.Sprintf("xref\n0 %d\n", len(objects)+1))
	buffer.WriteString("0000000000 65535 f \n")
	for index := 1; index <= len(objects); index++ {
		buffer.WriteString(fmt.Sprintf("%010d 00000 n \n", offsets[index]))
	}
	buffer.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefStart))

	return os.WriteFile(outputPath, buffer.Bytes(), 0o644)
}

func parsePDFObjects(data []byte) ([]pdfObject, error) {
	matches := pdfObjectPattern.FindAllStringSubmatchIndex(string(data), -1)
	if len(matches) == 0 {
		return nil, errors.New("invalid pdf: no objects found")
	}

	objects := make([]pdfObject, 0, len(matches))
	for index, match := range matches {
		start := match[0]
		headerEnd := match[1]
		oldID, err := strconv.Atoi(string(data[match[2]:match[3]]))
		if err != nil {
			return nil, err
		}

		nextStart := len(data)
		if index+1 < len(matches) {
			nextStart = matches[index+1][0]
		}

		end, err := findPDFObjectEnd(data, start, nextStart)
		if err != nil {
			return nil, err
		}
		if end <= start || end > len(data) {
			return nil, errors.New("invalid pdf object boundaries")
		}

		body := append([]byte(nil), data[headerEnd:end-len("endobj")]...)
		analysis := string(body)
		objects = append(objects, pdfObject{
			oldID:     oldID,
			body:      body,
			isCatalog: strings.Contains(analysis, "/Type /Catalog"),
			isPages:   strings.Contains(analysis, "/Type /Pages"),
			isPage:    strings.Contains(analysis, "/Type /Page") && !strings.Contains(analysis, "/Type /Pages"),
		})
	}

	return objects, nil
}

func findPDFObjectEnd(data []byte, start int, nextStart int) (int, error) {
	slice := data[start:nextStart]
	streamIndex := bytes.Index(slice, []byte("\nstream"))
	if streamIndex < 0 {
		streamIndex = bytes.Index(slice, []byte("\r\nstream"))
	}
	if streamIndex >= 0 {
		streamStart := start + streamIndex
		endStreamIndex := bytes.Index(data[streamStart:], []byte("endstream"))
		if endStreamIndex < 0 {
			return 0, errors.New("invalid pdf: missing endstream")
		}
		endStreamIndex += streamStart
		endObjIndex := bytes.Index(data[endStreamIndex:], []byte("endobj"))
		if endObjIndex < 0 {
			return 0, errors.New("invalid pdf: missing endobj after stream")
		}
		return endStreamIndex + endObjIndex + len("endobj"), nil
	}

	endObjIndex := bytes.Index(slice, []byte("endobj"))
	if endObjIndex < 0 {
		return 0, errors.New("invalid pdf: missing endobj")
	}
	return start + endObjIndex + len("endobj"), nil
}

func rewritePDFObjectBody(body []byte, mapping map[int]int) []byte {
	if len(body) == 0 {
		return body
	}

	streamIndex := bytes.Index(body, []byte("\nstream"))
	if streamIndex < 0 {
		streamIndex = bytes.Index(body, []byte("\r\nstream"))
	}
	if streamIndex < 0 {
		return replacePDFReferences(body, mapping)
	}

	prefix := replacePDFReferences(body[:streamIndex], mapping)
	return append(prefix, body[streamIndex:]...)
}

func replacePDFReferences(input []byte, mapping map[int]int) []byte {
	rewritten := pdfRefPattern.ReplaceAllStringFunc(string(input), func(match string) string {
		parts := pdfRefPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		oldID, err := strconv.Atoi(parts[1])
		if err != nil {
			return match
		}
		newID, ok := mapping[oldID]
		if !ok {
			return match
		}
		return fmt.Sprintf("%d %s R", newID, parts[2])
	})
	return []byte(rewritten)
}

func withQueryParams(rawURL string, params map[string]string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	values := parsed.Query()
	for key, value := range params {
		values.Set(key, value)
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}
