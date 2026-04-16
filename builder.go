package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	renderhtml "github.com/yuin/goldmark/renderer/html"
)

type builtDeck struct {
	ChannelName string
	SourceName  string
	Title       string
	Slides      []builtSlide
	ImageAssets []string
}

type builtSlide struct {
	Index      int             `json:"index"`
	Title      string          `json:"title"`
	Template   string          `json:"template"`
	HTML       string          `json:"html"`
	PlainText  string          `json:"plainText"`
	NotesHTML  string          `json:"notesHtml"`
	NotesText  string          `json:"notesText"`
	Background slideBackground `json:"background"`
}

type slideBackground struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type buildResult struct {
	InputPath         string
	OutputDir         string
	PresentationPath  string
	NotesPath         string
	MetadataPath      string
	PresentationName  string
	PresentationBuilt string
}

var (
	frontmatterPattern   = regexp.MustCompile(`(?s)\A---\n.*?\n---\n*`)
	slideSplitPattern    = regexp.MustCompile(`\n\s*---\s*\n`)
	notesBlockPattern    = regexp.MustCompile(`(?s)%%(.*?)%%`)
	obsidianEmbedPattern = regexp.MustCompile(`!\[\[([^\]]+)\]\]`)
	mdLinkPattern        = regexp.MustCompile(`\[(.*?)\]\((.*?)\)`)
	inlineCodePattern    = regexp.MustCompile("`([^`]+)`")
	colorPattern         = regexp.MustCompile(`(?i)^(#[0-9a-f]{3,8}|rgba?\(.+\)|hsla?\(.+\)|[a-z]+)$`)
)

var markdownRenderer = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(renderhtml.WithUnsafe(), renderhtml.WithHardWraps()),
)

func buildPresentationFile(rootDir string, inputPath string) (buildResult, error) {
	resolvedInputPath, err := filepath.Abs(inputPath)
	if err != nil {
		return buildResult{}, err
	}

	sourceBytes, err := os.ReadFile(resolvedInputPath)
	if err != nil {
		return buildResult{}, fmt.Errorf("input file not found: %s", resolvedInputPath)
	}

	builtAt := time.Now().UTC().Format(time.RFC3339)
	deck, err := parseDeck(string(sourceBytes), resolvedInputPath, builtAt)
	if err != nil {
		return buildResult{}, err
	}

	outputName := strings.TrimSuffix(filepath.Base(resolvedInputPath), filepath.Ext(resolvedInputPath))
	outputDir := filepath.Join(rootDir, "presentations", outputName)
	outputBase := filepath.Join(outputDir, outputName)
	presentationPath := outputBase + ".html"
	notesPath := outputBase + ".notes.html"
	metadataPath := filepath.Join(outputDir, "presentation.json")

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return buildResult{}, err
	}

	if err := localizeDeckAssets(deck, resolvedInputPath, outputDir); err != nil {
		return buildResult{}, err
	}
	deck.ImageAssets = collectImageAssets(deck.Slides)

	if err := os.WriteFile(presentationPath, []byte(renderPresentationHTML(deck)), 0o644); err != nil {
		return buildResult{}, err
	}
	if err := os.WriteFile(notesPath, []byte(renderNotesHTML(deck)), 0o644); err != nil {
		return buildResult{}, err
	}

	metadataBytes, err := json.MarshalIndent(presentationMetadata{
		Name:             outputName,
		SourcePath:       resolvedInputPath,
		PresentationPath: presentationPath,
		NotesPath:        notesPath,
		BuiltAt:          builtAt,
	}, "", "  ")
	if err != nil {
		return buildResult{}, err
	}

	if err := os.WriteFile(metadataPath, metadataBytes, 0o644); err != nil {
		return buildResult{}, err
	}

	return buildResult{
		InputPath:         resolvedInputPath,
		OutputDir:         outputDir,
		PresentationPath:  presentationPath,
		NotesPath:         notesPath,
		MetadataPath:      metadataPath,
		PresentationName:  outputName,
		PresentationBuilt: builtAt,
	}, nil
}

func parseDeck(source string, inputPath string, createdAt string) (*builtDeck, error) {
	normalized := stripFrontmatter(strings.ReplaceAll(source, "\r\n", "\n"))
	title := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	slideSources := splitSlides(normalized)

	var coverNotes *builtSlide
	if len(slideSources) > 0 && isNotesOnlySlideSource(slideSources[0]) {
		parsed, err := parseSlide(slideSources[0], 0, inputPath)
		if err != nil {
			return nil, err
		}
		coverNotes = &parsed
		slideSources = slideSources[1:]
	}

	slides := make([]builtSlide, 0, len(slideSources)+1)
	slides = append(slides, buildCoverSlide(title, createdAt, coverNotes))
	for index, slideSource := range slideSources {
		parsed, err := parseSlide(slideSource, index+1, inputPath)
		if err != nil {
			return nil, err
		}
		parsed.Index = len(slides)
		slides = append(slides, parsed)
	}

	return &builtDeck{
		ChannelName: makeChannelName(inputPath),
		SourceName:  filepath.Base(inputPath),
		Title:       title,
		Slides:      slides,
		ImageAssets: []string{},
	}, nil
}

func buildCoverSlide(title string, createdAt string, notesSlide *builtSlide) builtSlide {
	formattedDate := formatMonthYear(createdAt)
	background := slideBackground{Type: "none", Value: ""}
	notesHTML := ""
	notesText := ""
	if notesSlide != nil {
		background = notesSlide.Background
		notesHTML = notesSlide.NotesHTML
		notesText = notesSlide.NotesText
	}

	return builtSlide{
		Index:    0,
		Title:    title,
		Template: "cover",
		HTML: strings.TrimSpace(fmt.Sprintf(`
      <div class="cover-slide">
        <p class="cover-slide-kicker">Presentation</p>
        <h1 class="cover-slide-title">%s</h1>
        <p class="cover-slide-date">%s</p>
      </div>
    `, escapeHTML(title), escapeHTML(formattedDate))),
		PlainText:  title + "\n" + formattedDate,
		NotesHTML:  notesHTML,
		NotesText:  notesText,
		Background: background,
	}
}

func formatMonthYear(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return parsed.Format("January 2006")
}

func stripFrontmatter(source string) string {
	return frontmatterPattern.ReplaceAllString(source, "")
}

func splitSlides(source string) []string {
	parts := slideSplitPattern.Split(source, -1)
	results := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			results = append(results, trimmed)
		}
	}
	return results
}

func isNotesOnlySlideSource(slideSource string) bool {
	withoutNotes := notesBlockPattern.ReplaceAllString(slideSource, "")
	return strings.TrimSpace(withoutNotes) == ""
}

func parseSlide(slideSource string, index int, inputPath string) (builtSlide, error) {
	blocks := notesBlockPattern.FindAllStringSubmatch(slideSource, -1)
	notesHTML, notesText := extractSpeakerNotes(blocks)
	metadata := parseSlideMetadata(blocks)
	visibleMarkdown := strings.TrimSpace(notesBlockPattern.ReplaceAllString(slideSource, ""))
	slideTitle := getSlideTitle(visibleMarkdown, index)
	templateName := strings.TrimSpace(metadata["template"])
	background := buildBackground(metadata["bg"])

	renderedHTML, err := markdownToHTML(visibleMarkdown)
	if err != nil {
		return builtSlide{}, err
	}

	switch templateName {
	case "marked-text":
		renderedHTML = applyMarkedTextTemplate(renderedHTML)
	case "image-right":
		renderedHTML = applyRightHalfImageTemplate(renderedHTML)
	case "center":
		renderedHTML = applyCenterTemplate(renderedHTML)
	}

	return builtSlide{
		Index:      index,
		Title:      slideTitle,
		Template:   templateName,
		HTML:       renderedHTML,
		PlainText:  markdownToPlainText(visibleMarkdown),
		NotesHTML:  notesHTML,
		NotesText:  notesText,
		Background: background,
	}, nil
}

func extractSpeakerNotes(blocks [][]string) (string, string) {
	notes := make([]string, 0)
	for _, match := range blocks {
		lines := strings.Split(match[1], "\n")
		plainLines := make([]string, 0, len(lines))
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || isMetadataLine(trimmed) {
				continue
			}
			plainLines = append(plainLines, trimmed)
		}
		if len(plainLines) > 0 {
			notes = append(notes, strings.Join(plainLines, "\n"))
		}
	}

	if len(notes) == 0 {
		return "", ""
	}

	joined := strings.Join(notes, "\n\n")
	htmlValue, err := markdownToHTML(joined)
	if err != nil {
		return "<p>" + escapeHTML(joined) + "</p>", joined
	}
	return htmlValue, joined
}

func parseSlideMetadata(blocks [][]string) map[string]string {
	metadata := make(map[string]string)
	for _, match := range blocks {
		lines := strings.Split(match[1], "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if !isMetadataLine(trimmed) {
				continue
			}
			key, value, ok := parseMetadataLine(trimmed)
			if ok {
				metadata[key] = value
			}
		}
	}
	return metadata
}

func isMetadataLine(line string) bool {
	key, _, ok := parseMetadataLine(line)
	return ok && key != ""
}

func parseMetadataLine(line string) (string, string, bool) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key := strings.ToLower(strings.TrimSpace(parts[0]))
	value := strings.TrimSpace(parts[1])
	if key == "" || value == "" {
		return "", "", false
	}
	return key, stripOptionalQuotes(value), true
}

func stripOptionalQuotes(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func buildBackground(rawValue string) slideBackground {
	value := strings.TrimSpace(rawValue)
	// Strip Obsidian wikilink syntax: [[filename.png]] → filename.png
	if strings.HasPrefix(value, "[[") && strings.HasSuffix(value, "]]") {
		value = strings.TrimSpace(value[2 : len(value)-2])
		// Also strip any display alias: [[file.png|alias]] → file.png
		if pipe := strings.Index(value, "|"); pipe >= 0 {
			value = strings.TrimSpace(value[:pipe])
		}
	}
	if value == "" {
		return slideBackground{Type: "none", Value: ""}
	}
	if isColorValue(value) {
		return slideBackground{Type: "color", Value: value}
	}
	if isVideoAsset(value) {
		return slideBackground{Type: "video", Value: value}
	}
	return slideBackground{Type: "image", Value: value}
}

func isVideoAsset(value string) bool {
	extension := strings.ToLower(filepath.Ext(strings.SplitN(value, "?", 2)[0]))
	switch extension {
	case ".mp4", ".webm", ".ogg", ".mov":
		return true
	default:
		return false
	}
}

func isColorValue(value string) bool {
	return colorPattern.MatchString(strings.TrimSpace(value))
}

func markdownToHTML(markdown string) (string, error) {
	preprocessed := preprocessMarkdown(markdown)
	var output bytes.Buffer
	if err := markdownRenderer.Convert([]byte(preprocessed), &output); err != nil {
		return "", err
	}
	rendered := strings.TrimSpace(output.String())
	rendered = strings.ReplaceAll(rendered, "<table>", `<div class="table-wrapper"><table>`)
	rendered = strings.ReplaceAll(rendered, "</table>", "</table></div>")
	rendered = transformCallouts(rendered)
	return rendered, nil
}

func preprocessMarkdown(markdown string) string {
	return obsidianEmbedPattern.ReplaceAllStringFunc(markdown, func(match string) string {
		parts := obsidianEmbedPattern.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}

		rawTarget := strings.TrimSpace(parts[1])
		targetParts := strings.Split(rawTarget, "|")
		target := strings.TrimSpace(targetParts[0])
		if target == "" {
			return match
		}

		if isVideoAsset(target) {
			return fmt.Sprintf(`<video class="slide-media" src="%s" autoplay muted playsinline preload="metadata"></video>`, escapeAttribute(target))
		}

		return fmt.Sprintf(`<img class="slide-media" src="%s" alt="">`, escapeAttribute(target))
	})
}

func transformCallouts(input string) string {
	root := parseHTMLFragment(input)
	if root == nil {
		return input
	}

	for child := root.FirstChild; child != nil; child = child.NextSibling {
		transformCalloutNode(child)
	}
	return renderFragmentChildren(root)
}

func transformCalloutNode(node *xhtml.Node) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		transformCalloutNode(child)
	}

	if node.Type != xhtml.ElementNode || node.Data != "blockquote" {
		return
	}
	firstParagraph := firstElementChild(node)
	if firstParagraph == nil || firstParagraph.Data != "p" {
		return
	}

	text := strings.TrimSpace(nodeText(firstParagraph))
	if !strings.HasPrefix(strings.ToUpper(text), "[!") {
		return
	}

	remainder := strings.TrimSpace(strings.TrimPrefix(text, "[!"))
	endIndex := strings.Index(remainder, "]")
	if endIndex < 0 {
		return
	}

	calloutType := strings.ToLower(strings.TrimSpace(remainder[:endIndex]))

	// Everything after the closing "]". When there's no blank line between the
	// marker and the body (e.g. "> [!TIP]\n> body"), goldmark puts both in a
	// single <p> separated by "\n". Split on the first newline so the part
	// before is the optional custom title and the part after is inline body text.
	afterMarker := strings.TrimLeft(remainder[endIndex+1:], " \t")
	titlePart, bodyInline, hasInlineBody := strings.Cut(afterMarker, "\n")
	calloutTitle := strings.TrimSpace(titlePart)
	if calloutTitle == "" {
		calloutTitle = strings.Title(calloutType)
	}
	bodyInline = strings.TrimSpace(bodyInline)

	for firstParagraph.FirstChild != nil {
		firstParagraph.RemoveChild(firstParagraph.FirstChild)
	}
	node.RemoveChild(firstParagraph)
	node.Data = "aside"
	node.Attr = appendClass(node.Attr, "callout")
	node.Attr = appendClass(node.Attr, "callout-"+calloutType)

	titleNode := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", Attr: []xhtml.Attribute{{Key: "class", Val: "callout-title"}}}
	titleStrong := &xhtml.Node{Type: xhtml.ElementNode, Data: "strong"}
	titleStrong.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: calloutTitle})
	titleNode.AppendChild(titleStrong)

	bodyNode := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", Attr: []xhtml.Attribute{{Key: "class", Val: "callout-body"}}}

	// If there was inline body text on the same paragraph as the marker, inject
	// it as the first <p> in the body before any separate block children.
	if hasInlineBody && bodyInline != "" {
		inlineP := &xhtml.Node{Type: xhtml.ElementNode, Data: "p"}
		inlineP.AppendChild(&xhtml.Node{Type: xhtml.TextNode, Data: bodyInline})
		bodyNode.AppendChild(inlineP)
	}

	for node.FirstChild != nil {
		child := node.FirstChild
		node.RemoveChild(child)
		bodyNode.AppendChild(child)
	}

	node.AppendChild(titleNode)
	node.AppendChild(bodyNode)
}

func firstElementChild(node *xhtml.Node) *xhtml.Node {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == xhtml.ElementNode {
			return child
		}
	}
	return nil
}

func nodeText(node *xhtml.Node) string {
	var builder strings.Builder
	var walk func(*xhtml.Node)
	walk = func(current *xhtml.Node) {
		if current.Type == xhtml.TextNode {
			builder.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}

func appendClass(attributes []xhtml.Attribute, value string) []xhtml.Attribute {
	for index, attribute := range attributes {
		if attribute.Key == "class" {
			if attribute.Val == "" {
				attributes[index].Val = value
			} else if !strings.Contains(attribute.Val, value) {
				attributes[index].Val += " " + value
			}
			return attributes
		}
	}
	return append(attributes, xhtml.Attribute{Key: "class", Val: value})
}

func applyCenterTemplate(rendered string) string {
	headingEnd := strings.Index(strings.ToLower(rendered), "</h1>")
	if headingEnd < 0 {
		return rendered
	}
	headingEnd += len("</h1>")
	heading := rendered[:headingEnd]
	remainder := strings.TrimSpace(rendered[headingEnd:])
	if remainder == "" {
		return heading
	}
	return heading + "\n<div class=\"center-template-body\">\n" + remainder + "\n</div>"
}

func applyRightHalfImageTemplate(rendered string) string {
	imagePattern := regexp.MustCompile(`<img[^>]*src="([^"]+)"[^>]*alt="([^"]*)"[^>]*>`)
	match := imagePattern.FindStringSubmatch(rendered)
	if len(match) < 2 {
		return rendered
	}

	mediaSource := match[1]
	mediaAlt := ""
	if len(match) > 2 {
		mediaAlt = match[2]
	}
	copyHTML := strings.Replace(rendered, match[0], "", 1)
	copyHTML = regexp.MustCompile(`(?s)<p>\s*</p>`).ReplaceAllString(copyHTML, "")
	copyHTML = strings.TrimSpace(copyHTML)
	panel := fmt.Sprintf(`<div class="right-half-image-panel" role="img" aria-label="%s" style="background-image:url('%s');"></div>`, escapeAttribute(mediaAlt), escapeAttribute(mediaSource))
	return `<div class="right-half-image-layout">
      <div class="right-half-image-copy">
        ` + copyHTML + `
      </div>
      ` + panel + `
    </div>`
}

// bgWrapBlockTags are elements whose entire child list is wrapped in one bg-wrap span.
var bgWrapBlockTags = map[string]bool{
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"p": true, "center": true,
}

// bgWrapHTMLBlockTags are block-level tags whose presence inside a <li> signals
// that the li has block children (so we must not wrap the whole li, only its
// leading inline text before the first block child).
var bgWrapHTMLBlockTags = map[string]bool{
	"p": true, "ul": true, "ol": true, "div": true, "blockquote": true,
	"aside": true, "pre": true, "table": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true,
}

// applyMarkedTextTemplate wraps the inline content of block-level elements in
// a <span class="bg-wrap"> so that display:inline + box-decoration-break:clone
// gives each wrapped line its own background box, including in nested lists.
func applyMarkedTextTemplate(rendered string) string {
	root := parseHTMLFragment(rendered)
	if root == nil {
		return rendered
	}
	bgWrapNode(root)
	return renderFragmentChildren(root)
}

// bgWrapNode walks the tree post-order and wraps content in bg-wrap spans.
func bgWrapNode(node *xhtml.Node) {
	// Recurse first so nested lists are handled before their parents.
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		bgWrapNode(child)
	}

	if node.Type != xhtml.ElementNode {
		return
	}

	if bgWrapBlockTags[node.Data] {
		bgWrapAllChildren(node)
		return
	}

	if node.Data == "li" {
		bgWrapLiInlinePrefix(node)
	}
}

// bgWrapAllChildren wraps every child of parent in a single bg-wrap span.
func bgWrapAllChildren(parent *xhtml.Node) {
	if parent.FirstChild == nil {
		return
	}
	var children []*xhtml.Node
	for c := parent.FirstChild; c != nil; c = c.NextSibling {
		children = append(children, c)
	}
	for _, c := range children {
		parent.RemoveChild(c)
	}
	span := &xhtml.Node{
		Type: xhtml.ElementNode,
		Data: "span",
		Attr: []xhtml.Attribute{{Key: "class", Val: "bg-wrap"}},
	}
	for _, c := range children {
		span.AppendChild(c)
	}
	parent.AppendChild(span)
}

// bgWrapLiInlinePrefix wraps the leading inline children of a <li> (everything
// before the first block-level child) in a bg-wrap span, leaving any nested
// <ul>/<ol> and their already-wrapped items outside the span.
func bgWrapLiInlinePrefix(li *xhtml.Node) {
	// Collect consecutive leading inline (non-block) children.
	var inline []*xhtml.Node
	for c := li.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && bgWrapHTMLBlockTags[c.Data] {
			break
		}
		inline = append(inline, c)
	}

	// Only wrap if there is non-whitespace inline content.
	hasText := false
	for _, c := range inline {
		if c.Type == xhtml.TextNode && strings.TrimSpace(c.Data) != "" {
			hasText = true
			break
		}
		if c.Type == xhtml.ElementNode {
			hasText = true
			break
		}
	}
	if !hasText {
		return
	}

	firstBlockChild := li.FirstChild
	for _, c := range inline {
		firstBlockChild = firstBlockChild.NextSibling
		li.RemoveChild(c)
	}

	span := &xhtml.Node{
		Type: xhtml.ElementNode,
		Data: "span",
		Attr: []xhtml.Attribute{{Key: "class", Val: "bg-wrap"}},
	}
	for _, c := range inline {
		span.AppendChild(c)
	}
	li.InsertBefore(span, firstBlockChild)
}

func markdownToPlainText(markdown string) string {
	lines := strings.Split(markdown, "\n")
	output := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			output = append(output, "")
			continue
		}

		plain := trimmed
		plain = strings.TrimLeft(plain, "#>")
		plain = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(plain, "- "), "* "), "+ "))
		plain = regexp.MustCompile(`^\d+\.\s+`).ReplaceAllString(plain, "")
		plain = inlineCodePattern.ReplaceAllString(plain, "$1")
		plain = mdLinkPattern.ReplaceAllString(plain, "$1")
		plain = obsidianEmbedPattern.ReplaceAllStringFunc(plain, func(value string) string {
			match := obsidianEmbedPattern.FindStringSubmatch(value)
			if len(match) < 2 {
				return value
			}
			return match[1]
		})
		plain = strings.ReplaceAll(plain, "**", "")
		plain = strings.ReplaceAll(plain, "__", "")
		plain = strings.ReplaceAll(plain, "*", "")
		plain = strings.ReplaceAll(plain, "_", "")
		output = append(output, plain)
	}

	return strings.TrimSpace(strings.Join(output, "\n"))
}

func getSlideTitle(markdown string, index int) string {
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
	}
	return fmt.Sprintf("Slide %d", index+1)
}

func localizeDeckAssets(deck *builtDeck, inputPath string, outputDir string) error {
	assetsDir := filepath.Join(outputDir, "assets")
	copyCache := make(map[string]string)
	usedNames := make(map[string]struct{})

	for index := range deck.Slides {
		deck.Slides[index].HTML = rewriteHTMLAssetSources(deck.Slides[index].HTML, inputPath, assetsDir, copyCache, usedNames)
		deck.Slides[index].NotesHTML = rewriteHTMLAssetSources(deck.Slides[index].NotesHTML, inputPath, assetsDir, copyCache, usedNames)
		if deck.Slides[index].Background.Type == "image" || deck.Slides[index].Background.Type == "video" {
			localized, err := localizeAssetValue(deck.Slides[index].Background.Value, inputPath, assetsDir, copyCache, usedNames)
			if err != nil {
				return err
			}
			deck.Slides[index].Background.Value = localized
		}
	}

	return nil
}

func rewriteHTMLAssetSources(input string, inputPath string, assetsDir string, copyCache map[string]string, usedNames map[string]struct{}) string {
	stylePattern := regexp.MustCompile(`background-image:url\('([^']+)'\)`)
	input = stylePattern.ReplaceAllStringFunc(input, func(match string) string {
		submatch := stylePattern.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}
		localized, err := localizeAssetValue(submatch[1], inputPath, assetsDir, copyCache, usedNames)
		if err != nil || localized == "" {
			return match
		}
		return strings.Replace(match, submatch[1], localized, 1)
	})

	root := parseHTMLFragment(input)
	if root == nil {
		return input
	}

	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && (node.Data == "img" || node.Data == "video") {
			for index, attribute := range node.Attr {
				if attribute.Key != "src" {
					continue
				}
				localized, err := localizeAssetValue(attribute.Val, inputPath, assetsDir, copyCache, usedNames)
				if err == nil && localized != "" {
					node.Attr[index].Val = localized
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	for child := root.FirstChild; child != nil; child = child.NextSibling {
		walk(child)
	}
	return renderFragmentChildren(root)
}

func localizeAssetValue(value string, inputPath string, assetsDir string, copyCache map[string]string, usedNames map[string]struct{}) (string, error) {
	if value == "" || isRemoteAsset(value) || isColorValue(value) {
		return value, nil
	}

	sourcePath := resolveLocalAssetPath(value, inputPath)
	if sourcePath == "" {
		return value, nil
	}

	if cached, ok := copyCache[sourcePath]; ok {
		return cached, nil
	}

	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		return "", err
	}

	outputName := allocateAssetName(filepath.Base(sourcePath), usedNames)
	outputPath := filepath.Join(assetsDir, outputName)
	if err := copyFile(outputPath, sourcePath); err != nil {
		return "", err
	}

	relativePath := filepath.ToSlash(filepath.Join("assets", outputName))
	copyCache[sourcePath] = relativePath
	return relativePath, nil
}

func allocateAssetName(baseName string, usedNames map[string]struct{}) string {
	candidate := baseName
	extension := filepath.Ext(baseName)
	stem := strings.TrimSuffix(baseName, extension)
	counter := 2
	for {
		if _, exists := usedNames[candidate]; !exists {
			usedNames[candidate] = struct{}{}
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", stem, counter, extension)
		counter++
	}
}

func resolveLocalAssetPath(reference string, inputPath string) string {
	normalized := normalizeAssetReference(reference)
	if normalized == "" {
		return ""
	}

	if filepath.IsAbs(normalized) && fileExists(normalized) {
		return normalized
	}

	sourceDir := filepath.Dir(inputPath)
	directPath := filepath.Clean(filepath.Join(sourceDir, normalized))
	if fileExists(directPath) {
		return directPath
	}

	vaultRoot := findVaultRoot(sourceDir)
	if vaultRoot == "" {
		return ""
	}

	vaultPath := filepath.Clean(filepath.Join(vaultRoot, normalized))
	if fileExists(vaultPath) {
		return vaultPath
	}

	return findFileByName(vaultRoot, filepath.Base(normalized))
}

func normalizeAssetReference(reference string) string {
	trimmed := strings.TrimSpace(reference)
	trimmed = strings.SplitN(trimmed, "|", 2)[0]
	trimmed = strings.SplitN(trimmed, "#", 2)[0]
	return strings.TrimSpace(trimmed)
}

func isRemoteAsset(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:")
}

func findVaultRoot(startDir string) string {
	current := startDir
	for {
		if fileExists(filepath.Join(current, ".obsidian")) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func findFileByName(rootDir string, fileName string) string {
	var found string
	_ = filepath.WalkDir(rootDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if strings.HasPrefix(name, ".") && name != ".obsidian" {
				return filepath.SkipDir
			}
			if name == "node_modules" || name == "presentations" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == fileName {
			found = path
		}
		return nil
	})
	return found
}

func copyFile(destination string, source string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}

func collectImageAssets(slides []builtSlide) []string {
	assets := make(map[string]struct{})
	srcPattern := regexp.MustCompile(`<(?:img|video)[^>]+\ssrc="([^"]+)"`)
	backgroundPattern := regexp.MustCompile(`background-image:url\('([^']+)'\)`)
	for _, slide := range slides {
		if slide.Background.Type == "image" && slide.Background.Value != "" {
			assets[slide.Background.Value] = struct{}{}
		}
		matches := srcPattern.FindAllStringSubmatch(slide.HTML, -1)
		for _, match := range matches {
			if len(match) > 1 {
				assets[match[1]] = struct{}{}
			}
		}
		backgroundMatches := backgroundPattern.FindAllStringSubmatch(slide.HTML, -1)
		for _, match := range backgroundMatches {
			if len(match) > 1 {
				assets[match[1]] = struct{}{}
			}
		}
	}
	results := make([]string, 0, len(assets))
	for asset := range assets {
		results = append(results, asset)
	}
	sort.Strings(results)
	return results
}

func remoteImagePreloadTags(assets []string) string {
	var tags []string
	for _, asset := range assets {
		if isRemoteAsset(asset) && !isVideoAsset(asset) {
			tags = append(tags, fmt.Sprintf(`    <link rel="preload" as="image" href="%s">`, escapeAttribute(asset)))
		}
	}
	if len(tags) == 0 {
		return ""
	}
	return "\n" + strings.Join(tags, "\n")
}

func makeChannelName(inputPath string) string {
	lower := strings.ToLower(inputPath)
	var builder strings.Builder
	for _, char := range lower {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

func renderSlideMarkup(slide builtSlide, totalSlides int) string {
	classes := []string{"slide"}
	switch slide.Template {
	case "cover":
		classes = append(classes, "template-cover")
	case "marked-text":
		classes = append(classes, "template-marked-text")
	case "image-right":
		classes = append(classes, "template-image-right")
	case "center":
		classes = append(classes, "template-center")
	}

	background := renderSlideBackground(slide.Background)
	return fmt.Sprintf(`<section class="%s" data-slide-index="%d" aria-label="Slide %d of %d">
      %s
      <div class="slide-content">
        %s
      </div>
    </section>`, strings.Join(classes, " "), slide.Index, slide.Index+1, totalSlides, background, slide.HTML)
}

func renderSlideBackground(background slideBackground) string {
	switch background.Type {
	case "color":
		return fmt.Sprintf(`<div class="slide-background" style="background:%s;"></div>`, escapeAttribute(background.Value))
	case "image":
		return fmt.Sprintf(`<div class="slide-background slide-background-image" style="background-image:url('%s');"></div>`, escapeAttribute(background.Value))
	case "video":
		return fmt.Sprintf(`<div class="slide-background"><video class="slide-background-video" src="%s" autoplay muted loop playsinline preload="auto"></video></div>`, escapeAttribute(background.Value))
	default:
		return ""
	}
}

func renderPresentationHTML(deck *builtDeck) string {
	slideMarkup := make([]string, 0, len(deck.Slides))
	for _, slide := range deck.Slides {
		slideMarkup = append(slideMarkup, renderSlideMarkup(slide, len(deck.Slides)))
	}
	slidesJSON := mustMarshalJSON(deck.Slides)
	imageAssetsJSON := mustMarshalJSON(deck.ImageAssets)

	return strings.TrimSpace(fmt.Sprintf(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>%s</title>%s
    <style>
%s
    </style>
  </head>
  <body>
    <main class="slide-deck" id="deck">
      %s
    </main>
    <button class="fullscreen-toggle" id="fullscreen-toggle" type="button">Fullscreen</button>
    <div class="presentation-title">%s</div>
    <div class="slide-number" id="slide-number"></div>
    <script>
      const CHANNEL_NAME = %s;
      const SLIDES = %s;
      const IMAGE_ASSETS = %s;
      const MERMAID_MODULE_URL = "https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs";
      const params = new URLSearchParams(window.location.search);
      const exportPdfMode = params.get("exportPdf") === "1";
      const previewMode = params.get("preview") === "1";
      const channel = (exportPdfMode || previewMode) ? null : new BroadcastChannel(CHANNEL_NAME);
      const slideElements = Array.from(document.querySelectorAll(".slide"));
      window.__presentationSlideCount = slideElements.length;
      const slideNumber = document.getElementById("slide-number");
      const fullscreenToggle = document.getElementById("fullscreen-toggle");
      let currentIndex = 0;
      let mermaidApiPromise = null;
      let fullscreenHideTimer = null;

      function clampIndex(index) {
        return Math.max(0, Math.min(index, slideElements.length - 1));
      }

      function readIndexFromQuery() {
        const value = Number(new URLSearchParams(window.location.search).get("slide"));
        return Number.isInteger(value) ? value - 1 : 0;
      }

      function writeIndexToQuery(index) {
        const nextUrl = new URL(window.location.href);
        nextUrl.searchParams.set("slide", String(index + 1));
        window.history.replaceState({}, "", nextUrl);
      }

      function prefetchImages() {
        IMAGE_ASSETS.forEach((asset) => {
          const image = new Image();
          image.decoding = "async";
          image.src = asset;
        });
      }

      function escapeHtmlForScript(value) {
        return value
          .replace(/&/g, "&amp;")
          .replace(/</g, "&lt;")
          .replace(/>/g, "&gt;")
          .replace(/"/g, "&quot;")
          .replace(/'/g, "&#39;");
      }

      async function getMermaidApi() {
        if (!mermaidApiPromise) {
          mermaidApiPromise = import(MERMAID_MODULE_URL)
            .then((module) => {
              const mermaid = module.default || module;
              mermaid.initialize({ startOnLoad: false, securityLevel: "loose", theme: "neutral" });
              return mermaid;
            })
            .catch((error) => {
              console.error("Could not load Mermaid.", error);
              return null;
            });
        }
        return mermaidApiPromise;
      }

      function showMermaidError(container, error) {
        container.innerHTML = '<code class="mermaid-error">' + escapeHtmlForScript(String(error && error.message ? error.message : error)) + '</code>';
      }

      async function renderMermaidIn(root) {
        const mermaidBlocks = Array.from(root.querySelectorAll('pre code.language-mermaid')).map((node) => node.parentElement);
        if (!mermaidBlocks.length) {
          return;
        }
        const mermaid = await getMermaidApi();
        if (!mermaid) {
          mermaidBlocks.forEach((block) => showMermaidError(block, 'Mermaid could not be loaded.'));
          return;
        }
        for (const block of mermaidBlocks) {
          if (block.dataset.mermaidRendered === 'true') {
            continue;
          }
          const sourceNode = block.querySelector('code');
          const source = sourceNode ? sourceNode.textContent || '' : block.textContent || '';
          try {
            const renderId = 'mermaid-' + currentIndex + '-' + Math.random().toString(36).slice(2, 10);
            const rendered = await mermaid.render(renderId, source);
            block.classList.add('mermaid');
            block.innerHTML = rendered.svg;
            block.dataset.mermaidRendered = 'true';
          } catch (error) {
            console.error('Could not render Mermaid diagram.', error);
            showMermaidError(block, error);
          }
        }
      }

      function updateFullscreenButton() {
        if (!fullscreenToggle || typeof document.documentElement.requestFullscreen !== 'function') {
          if (fullscreenToggle) {
            fullscreenToggle.style.display = 'none';
          }
          return;
        }
        fullscreenToggle.textContent = document.fullscreenElement ? 'Exit Fullscreen' : 'Fullscreen';
      }

      function setFullscreenButtonVisible(visible) {
        if (!fullscreenToggle || exportPdfMode) {
          return;
        }

        fullscreenToggle.classList.toggle('is-visible', visible);
      }

      function scheduleFullscreenButtonHide() {
        clearTimeout(fullscreenHideTimer);
        fullscreenHideTimer = setTimeout(() => {
          setFullscreenButtonVisible(false);
        }, 600);
      }

      function shouldShowFullscreenButton(event) {
        return window.innerWidth - event.clientX <= 88 && event.clientY <= 72;
      }

      async function toggleFullscreen() {
        if (document.fullscreenElement) {
          await document.exitFullscreen();
        } else if (typeof document.documentElement.requestFullscreen === 'function') {
          await document.documentElement.requestFullscreen();
        }
        updateFullscreenButton();
      }

      async function goToSlide(index, broadcast) {
        currentIndex = clampIndex(index);
        slideElements.forEach((slideElement, slideIndex) => {
          slideElement.classList.toggle('is-active', slideIndex === currentIndex);
        });
        if (slideNumber) {
          slideNumber.textContent = (currentIndex + 1) + ' / ' + slideElements.length;
        }
        document.title = SLIDES[currentIndex] ? SLIDES[currentIndex].title + ' - ' + %s : %s;
        writeIndexToQuery(currentIndex);
        const activeSlide = slideElements[currentIndex];
        if (activeSlide) {
          await renderMermaidIn(activeSlide);
        }
        if (!previewMode && !exportPdfMode) localStorage.setItem(CHANNEL_NAME + ':index', currentIndex);
        if (broadcast && channel) {
          channel.postMessage({ type: 'slide-change', index: currentIndex });
        }
      }

      async function waitForElementImages(root) {
        const images = Array.from(root.querySelectorAll('img'));
        await Promise.all(images.map((image) => {
          if (image.complete) {
            return Promise.resolve();
          }
          return new Promise((resolve) => {
            image.addEventListener('load', resolve, { once: true });
            image.addEventListener('error', resolve, { once: true });
          });
        }));
      }

      function waitForEvent(target, eventName) {
        return new Promise((resolve) => {
          target.addEventListener(eventName, resolve, { once: true });
        });
      }

      async function captureVideoPosterFrame(video) {
        const poster = (video.getAttribute('poster') || '').trim();
        if (poster) {
          return poster;
        }

        if (video.readyState < 2) {
          await waitForEvent(video, 'loadeddata');
        }

        try {
          video.pause();
          video.currentTime = 0;
          await new Promise((resolve) => setTimeout(resolve, 50));
        } catch (error) {
          console.warn('Could not seek video for export.', error);
        }

        const canvas = document.createElement('canvas');
        const width = video.videoWidth || video.clientWidth || 1;
        const height = video.videoHeight || video.clientHeight || 1;
        canvas.width = width;
        canvas.height = height;
        const context = canvas.getContext('2d');
        if (!context) {
          return '';
        }

        try {
          context.drawImage(video, 0, 0, width, height);
          return canvas.toDataURL('image/png');
        } catch (error) {
          console.warn('Could not capture first video frame for export.', error);
          return poster;
        }
      }

      async function replaceVideosForPdf(root) {
        const videos = Array.from(root.querySelectorAll('video'));
        await Promise.all(videos.map(async (video) => {
          const posterSource = await captureVideoPosterFrame(video);
          if (!posterSource) {
            return;
          }

          const image = document.createElement('img');
          image.src = posterSource;
          image.alt = video.getAttribute('aria-label') || video.getAttribute('alt') || '';
          image.className = video.className;

          const parent = video.parentElement;
          if (!parent) {
            return;
          }
          parent.replaceChild(image, video);
        }));
      }

      async function prepareExportPdf() {
        if (!exportPdfMode) {
          return;
        }

        const activeSlide = document.querySelector('.slide.is-active');
        if (!activeSlide) {
          return;
        }

        await replaceVideosForPdf(activeSlide);
        await waitForElementImages(activeSlide);
        if (document.fonts && document.fonts.ready) {
          await document.fonts.ready;
        }
        const exportSlide = activeSlide.cloneNode(true);
        exportSlide.classList.add('is-active');
        exportSlide.style.width = '320mm';
        exportSlide.style.minHeight = '200mm';
        exportSlide.style.height = 'auto';
        exportSlide.style.overflow = 'visible';
        exportSlide.style.breakInside = 'avoid';
        exportSlide.style.pageBreakInside = 'avoid';
        document.body.innerHTML = '';
        document.body.appendChild(exportSlide);
        document.body.classList.add('export-mode');
        document.body.style.margin = '0';
        document.body.style.padding = '0';
        document.body.style.overflow = 'visible';
        document.documentElement.style.height = 'auto';
        await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        const exportBounds = exportSlide.getBoundingClientRect();
        const contentRoot = exportSlide.querySelector('.slide-content') || exportSlide;
        const contentBounds = contentRoot.getBoundingClientRect();
        let contentBottom = contentBounds.bottom;
        for (const element of contentRoot.querySelectorAll('*')) {
          const rect = element.getBoundingClientRect();
          contentBottom = Math.max(contentBottom, rect.bottom);
        }
        const baseHeight = Math.round(exportBounds.width * 10 / 16);
        const contentHeight = Math.ceil(Math.max(
          contentRoot.scrollHeight,
          contentRoot.offsetHeight,
          contentRoot.clientHeight,
          contentBottom - contentBounds.top
        ));
        const exportHeight = Math.max(baseHeight, contentHeight);
        window.__presentationExportGeometry = {
          width: Math.ceil(exportBounds.width),
          height: Math.ceil(exportHeight),
        };
      }

      function stepSlide(delta) {
        goToSlide(currentIndex + delta, true);
      }

      function setupLiveReload() {
        if (window.location.protocol !== 'http:' && window.location.protocol !== 'https:') {
          return;
        }
        const eventsUrl = new URL('/events', window.location.origin);
        eventsUrl.searchParams.set('name', %s);
        const source = new EventSource(eventsUrl);
        source.addEventListener('reload', () => window.location.reload());
      }

      if (!previewMode) {
        document.addEventListener('keydown', (event) => {
          if (['ArrowRight', 'PageDown', ' '].includes(event.key)) {
            event.preventDefault();
            stepSlide(1);
          }
          if (['ArrowLeft', 'PageUp'].includes(event.key)) {
            event.preventDefault();
            stepSlide(-1);
          }
        });

        document.addEventListener('fullscreenchange', updateFullscreenButton);
        document.addEventListener('mousemove', (event) => {
          if (shouldShowFullscreenButton(event)) {
            setFullscreenButtonVisible(true);
            scheduleFullscreenButtonHide();
            return;
          }
          scheduleFullscreenButtonHide();
        });
        document.addEventListener('mouseleave', () => {
          setFullscreenButtonVisible(false);
        });
        fullscreenToggle?.addEventListener('click', toggleFullscreen);
        channel?.addEventListener('message', (event) => {
          if (event.data && event.data.type === 'slide-change') {
            goToSlide(event.data.index, false);
          }
        });
      }

      if (exportPdfMode) {
        document.body.classList.add('export-mode');
      }
      if (previewMode) {
        document.body.classList.add('preview-mode');
      }

      if (!previewMode) prefetchImages();
      if (!previewMode) setupLiveReload();
      updateFullscreenButton();
      window.__presentationReady = (async () => {
        await goToSlide(readIndexFromQuery(), false);
        await prepareExportPdf();
      })();
    </script>
  </body>
</html>`, escapeHTML(deck.Title), remoteImagePreloadTags(deck.ImageAssets), sharedSlideStyles(), strings.Join(slideMarkup, "\n"), escapeHTML(deck.Title), mustMarshalJSONString(deck.ChannelName), slidesJSON, imageAssetsJSON, jsQuoted(deck.Title), jsQuoted(deck.Title), jsQuoted(deck.Title)))
}

func renderNotesHTML(deck *builtDeck) string {
	slidesJSON := mustMarshalJSON(deck.Slides)
	return strings.TrimSpace(fmt.Sprintf(`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>%s Notes</title>
    <style>
%s

      body.notes-window {
        overflow: auto;
        padding: 1.5rem;
        background: #f5f7fb;
      }

      .notes-shell {
        display: grid;
        grid-template-columns: minmax(18rem, 1fr) minmax(18rem, 32rem);
        gap: 1.25rem;
        align-items: start;
      }

      .notes-left {
        display: flex;
        flex-direction: column;
        gap: 1.25rem;
      }

      .notes-panel {
        padding: 1.25rem;
        border: 0.0625rem solid var(--border-color);
        background: rgba(255, 255, 255, 0.96);
        box-shadow: 0 1rem 2rem rgba(17, 17, 17, 0.06);
      }

      .notes-panel h1 {
        font-size: clamp(1.8rem, 3vw, 2.8rem);
      }

      .notes-panel h2 {
        font-size: clamp(1.2rem, 2vw, 1.8rem);
        margin-top: 0;
      }

      .notes-preview {
        font-size: 0.92rem;
      }

      .preview-container {
        position: relative;
        width: 100%%;
        aspect-ratio: 16 / 9;
        overflow: hidden;
        border: 0.0625rem solid var(--border-color);
        background: #fff;
      }

      .preview-iframe {
        position: absolute;
        top: 0;
        left: 0;
        width: 1280px;
        height: 720px;
        border: none;
        transform-origin: top left;
      }

      .notes-meta {
        margin-bottom: 0.75rem;
        font-size: 0.9rem;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: var(--muted-color);
      }

      .notes-empty {
        color: var(--muted-color);
      }

      @media (max-width: 64rem) {
        .notes-shell {
          grid-template-columns: 1fr;
        }
        .notes-left {
          gap: 1.25rem;
        }
      }
    </style>
  </head>
  <body class="notes-window">
    <div class="notes-shell">
      <section class="notes-panel notes-preview">
        <h2>Current Slide</h2>
        <div class="preview-container">
          <iframe class="preview-iframe" id="slide-preview-frame" frameborder="0"></iframe>
        </div>
      </section>
      <div class="notes-left">
        <section class="notes-panel">
          <div class="notes-meta" id="notes-meta"></div>
          <div id="notes-body" class="notes-body"></div>
        </section>
        <section class="notes-panel notes-preview">
          <h2>Next Slide</h2>
          <div class="preview-container">
            <iframe class="preview-iframe" id="next-slide-preview-frame" frameborder="0"></iframe>
          </div>
        </section>
      </div>
    </div>
    <script>
      const CHANNEL_NAME = %s;
      const SLIDES = %s;
      const channel = new BroadcastChannel(CHANNEL_NAME);
      const presentationPath = window.location.pathname.replace('.notes.html', '.html');
      let currentIndex = 0;

      function clampIndex(index) {
        return Math.max(0, Math.min(index, SLIDES.length - 1));
      }

      function readIndexFromQuery() {
        const value = Number(new URLSearchParams(window.location.search).get('slide'));
        return Number.isInteger(value) ? value - 1 : 0;
      }

      function writeIndexToQuery(index) {
        const nextUrl = new URL(window.location.href);
        nextUrl.searchParams.set('slide', String(index + 1));
        window.history.replaceState({}, '', nextUrl);
      }

      function renderPreview(slideIndex, frameId) {
        const frame = document.getElementById(frameId);
        if (!frame) return;
        if (slideIndex < 0 || slideIndex >= SLIDES.length) {
          frame.src = 'about:blank';
          return;
        }
        frame.src = presentationPath + '?slide=' + (slideIndex + 1) + '&preview=1';
      }

      function updatePreviewScale() {
        document.querySelectorAll('.preview-container').forEach(function(container) {
          const iframe = container.querySelector('.preview-iframe');
          if (iframe) {
            iframe.style.transform = 'scale(' + (container.clientWidth / 1280) + ')';
          }
        });
      }

      function goToSlide(index, broadcast) {
        currentIndex = clampIndex(index);
        const slide = SLIDES[currentIndex];
        document.getElementById('notes-meta').textContent = 'Slide ' + (currentIndex + 1) + ' / ' + SLIDES.length;
        document.getElementById('notes-body').innerHTML = slide.notesHtml || '<p class="notes-empty">No speaker notes for this slide.</p>';
        renderPreview(currentIndex, 'slide-preview-frame');
        renderPreview(currentIndex + 1, 'next-slide-preview-frame');
        writeIndexToQuery(currentIndex);
        localStorage.setItem(CHANNEL_NAME + ':index', currentIndex);
        if (broadcast) {
          channel.postMessage({ type: 'slide-change', index: currentIndex });
        }
      }

      function stepSlide(delta) {
        goToSlide(currentIndex + delta, true);
      }

      function setupLiveReload() {
        if (window.location.protocol !== 'http:' && window.location.protocol !== 'https:') {
          return;
        }
        const eventsUrl = new URL('/events', window.location.origin);
        eventsUrl.searchParams.set('name', %s);
        const source = new EventSource(eventsUrl);
        source.addEventListener('reload', () => window.location.reload());
      }

      document.addEventListener('keydown', (event) => {
        if (['ArrowRight', 'PageDown', ' '].includes(event.key)) {
          event.preventDefault();
          stepSlide(1);
        }
        if (['ArrowLeft', 'PageUp'].includes(event.key)) {
          event.preventDefault();
          stepSlide(-1);
        }
      });

      channel.addEventListener('message', (event) => {
        if (event.data && event.data.type === 'slide-change') {
          goToSlide(event.data.index, false);
        }
      });

      window.addEventListener('resize', updatePreviewScale);
      updatePreviewScale();
      setupLiveReload();
      const savedIndex = localStorage.getItem(CHANNEL_NAME + ':index');
      goToSlide(savedIndex !== null ? parseInt(savedIndex, 10) : readIndexFromQuery(), false);
    </script>
  </body>
</html>`, escapeHTML(deck.Title), sharedSlideStyles(), mustMarshalJSONString(deck.ChannelName), slidesJSON, jsQuoted(deck.Title)))
}

func sharedSlideStyles() string {
	return `
      :root {
        --slide-padding-x: 4.5rem;
        --slide-padding-y: 3rem;
        --headline-font: "Arial Narrow", Arial, sans-serif;
        --body-font: Inter, "Helvetica Neue", Helvetica, Arial, sans-serif;
        --text-color: #111111;
        --muted-color: #5b5b5b;
        --border-color: rgba(17, 17, 17, 0.12);
      }

      * { box-sizing: border-box; }

      html, body {
        width: 100%;
        height: 100%;
        margin: 0;
        background: #ffffff;
        color: var(--text-color);
        font-family: var(--body-font);
      }

      body { overflow: hidden; }
      body.export-mode { overflow: visible; }
      body.export-mode .slide-media {
        max-height: none;
      }

      h1, h2, h3, h4, h5, h6 {
        margin: 0 0 1rem;
        font-family: var(--headline-font);
        font-weight: 100;
        line-height: 0.95;
        letter-spacing: -0.03em;
      }

      h1 { font-size: clamp(3.2rem, 7vw, 6.6rem); }
      h2 { font-size: clamp(2.6rem, 5vw, 4.8rem); }
      h3 { font-size: clamp(2rem, 4vw, 3.4rem); }

      p, li, blockquote, code, center {
        font-size: clamp(2.05rem, 2.7vw, 2.55rem);
        line-height: 1.45;
      }

      center { display: block; }
      p, ul, ol, pre, blockquote { margin: 0 0 1rem; }
      ul, ol { padding-left: 1.35em; }

      a {
        color: #1557ff;
        text-decoration-thickness: 0.08em;
        text-underline-offset: 0.14em;
      }

      code { font-family: "SFMono-Regular", Menlo, Consolas, monospace; }
      :not(pre) > code {
        padding: 0.08em 0.28em;
        background: #f3f3f3;
      }

      pre {
        overflow-x: auto;
        padding: 1rem 1.2rem;
        border: 0.0625rem solid var(--border-color);
        background: #f3f3f3;
        white-space: pre-wrap;
        overflow-wrap: anywhere;
      }

      blockquote {
        padding: 1rem 1.25rem;
        border-left: 0.3rem solid #cbd5e1;
        background: #f8fafc;
        color: #1f2937;
      }

      .callout {
        margin: 2rem 0 1rem;
        padding: 1rem 1.25rem;
        border: 0.0625rem solid rgba(255, 255, 255, 0.18);
        border-left: 0.3rem solid var(--muted-color);
        background: #000000;
        color: #ffffff;
      }

      .callout-title {
        margin: 0 0 0.5rem;
        font-size: 0.95em;
        font-weight: 700;
        letter-spacing: 0.04em;
        text-transform: uppercase;
        color: rgba(255, 255, 255, 0.72);
      }

      .callout-note { border-left-color: #2563eb; }
      .callout-info { border-left-color: #0369a1; }
      .callout-warning { border-left-color: #b45309; }
      .callout-danger, .callout-error { border-left-color: #b91c1c; }
      .callout-success { border-left-color: #047857; }

      img, video { max-width: 100%; }

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

      .slide.is-active { display: block; }
      body.export-mode .slide {
        position: relative;
        display: none;
        min-height: 0;
        height: auto;
        overflow: visible;
      }

      body.export-mode .slide.is-active {
        display: block;
      }

      body.export-mode .slide-deck {
        width: 297mm;
        height: auto;
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
        padding-bottom: 3rem;
      }

      .slide.template-cover {
        display: none;
        align-items: flex-end;
        background:
          radial-gradient(circle at top left, rgba(21, 87, 255, 0.14), transparent 28%),
          linear-gradient(180deg, #faf7f1 0%, #ffffff 48%, #f2f5ff 100%);
      }

      .slide.template-cover.is-active,
      body.export-mode .slide.template-cover.is-active {
        display: flex;
      }

      .slide.template-cover .slide-content {
        display: flex;
        align-items: flex-end;
        min-height: 100%;
        max-width: 100%;
        padding-bottom: 0;
      }

      body.export-mode .slide.template-cover .slide-content {
        min-height: 0;
      }

      .cover-slide {
        width: min(100%, 64rem);
        padding: 0 0 4vh;
      }

      body.export-mode .cover-slide {
        padding-bottom: 3rem;
      }

      .cover-slide-kicker {
        margin-bottom: 1.2rem;
        font-size: 0.95rem;
        letter-spacing: 0.18em;
        text-transform: uppercase;
        color: var(--muted-color);
      }

      .cover-slide-date {
        font-size: clamp(1.1rem, 2vw, 1.5rem);
        color: var(--muted-color);
      }

      .slide.template-image-right,
      .slide.template-image-right .slide-content {
        padding: 0;
        max-width: 100%;
      }

      .right-half-image-layout {
        display: grid;
        grid-template-columns: minmax(0, 1fr) 50%;
        min-height: 100vh;
      }

      body.export-mode .right-half-image-layout {
        min-height: 0;
        align-items: stretch;
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

      body.export-mode .right-half-image-panel {
        min-height: 0;
      }

      .center-template-body {
        display: grid;
        place-items: center;
        min-height: calc(100vh - 14rem);
        text-align: center;
      }

      body.export-mode .center-template-body {
        min-height: 0;
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


      .slide.template-marked-text pre,
      .slide.template-marked-text code,
      .slide.template-marked-text table {
        background: #000000;
      }

      .slide.template-marked-text h1,
      .slide.template-marked-text h2,
      .slide.template-marked-text h3,
      .slide.template-marked-text h4,
      .slide.template-marked-text h5,
      .slide.template-marked-text h6,
      .slide.template-marked-text p,
      .slide.template-marked-text ul,
      .slide.template-marked-text ol,
      .slide.template-marked-text blockquote,
      .slide.template-marked-text center {
        display: block;
        margin-bottom: 0.7rem;
      }

      .slide.template-marked-text .bg-wrap {
        display: inline;
        background: #000000;
        padding: 0.18em 0.28em;
        line-height: 1.8;
        box-decoration-break: clone;
        -webkit-box-decoration-break: clone;
      }

      .slide.template-marked-text ul,
      .slide.template-marked-text ol {
        display: block;
      }

      .slide.template-marked-text li {
        display: block;
        padding: 0.12em 0.28em 0.12em 1.2em;
        margin-bottom: 0.35rem;
      }

      .slide.template-marked-text code,
      .slide.template-marked-text pre,
      .slide.template-marked-text pre code {
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

      ul, ol {
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

      .table-wrapper {
        width: 100%;
        overflow-x: auto;
        margin: 1.5rem 0;
      }

      table {
        width: 100%;
        border-collapse: collapse;
        font-size: 1.15em;
        background: #ffffff;
      }

      th, td {
        padding: 0.8rem 1rem;
        border: 1px solid #d9d9d9;
        vertical-align: top;
        color: #1f2328;
      }

      tbody tr:nth-child(odd) td { background: #f3f3f3; }

      pre.mermaid {
        display: flex;
        justify-content: center;
        align-items: center;
        overflow-x: auto;
        width: 100%;
        min-height: 32rem;
        padding: 2rem 0;
        background: transparent;
        border: 0;
      }

      .mermaid-error {
        white-space: pre-wrap;
        padding: 1rem;
        border: 0.0625rem solid #f0b8b8;
        background: #fff2f2;
        color: #8b1e1e;
        font-size: 0.95rem;
      }

      .slide-number,
      .presentation-title {
        position: fixed;
        bottom: 0.8rem;
        font-size: 0.85rem;
        letter-spacing: 0.08em;
        text-transform: uppercase;
        color: rgba(17, 17, 17, 0.56);
        z-index: 20;
      }

      .presentation-title { left: 1rem; }
      .slide-number { right: 1rem; }

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
        transition: opacity 120ms ease, background-color 120ms ease, color 120ms ease;
      }

      .fullscreen-toggle.is-visible {
        opacity: 1;
        pointer-events: auto;
      }

      body.export-mode .presentation-title,
      body.export-mode .slide-number,
      body.export-mode .fullscreen-toggle,
      body.preview-mode .presentation-title,
      body.preview-mode .slide-number,
      body.preview-mode .fullscreen-toggle {
        display: none !important;
      }

      @media (max-width: 56.25rem) {
        :root {
          --slide-padding-x: 1.4rem;
          --slide-padding-y: 1.6rem;
        }

        .slide-media { max-height: 44vh; }
        .right-half-image-layout { grid-template-columns: 1fr; gap: 1rem; }
        .right-half-image-panel { min-height: 35vh; }
      }
`
}

func mustMarshalJSON(value any) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(bytes)
}

func mustMarshalJSONString(value string) string {
	bytes, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(bytes)
}

func jsQuoted(value string) string {
	return mustMarshalJSONString(value)
}

func escapeHTML(value string) string {
	return html.EscapeString(value)
}

func escapeAttribute(value string) string {
	return html.EscapeString(value)
}

func parseHTMLFragment(input string) *xhtml.Node {
	context := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := xhtml.ParseFragment(strings.NewReader(input), context)
	if err != nil || len(nodes) == 0 {
		return nil
	}

	root := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", DataAtom: atom.Div}
	for _, node := range nodes {
		root.AppendChild(node)
	}
	return root
}

func renderFragmentChildren(root *xhtml.Node) string {
	var builder strings.Builder
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		builder.WriteString(renderNode(child))
	}
	return builder.String()
}

func renderNode(node *xhtml.Node) string {
	var builder strings.Builder
	_ = xhtml.Render(&builder, node)
	return builder.String()
}
