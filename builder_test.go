package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPresentationFile(t *testing.T) {
	rootDir := t.TempDir()
	sourceDir := filepath.Join(rootDir, "vault")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}

	imagePath := filepath.Join(sourceDir, "diagram.png")
	if err := os.WriteFile(imagePath, []byte("fake-image"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	sourcePath := filepath.Join(sourceDir, "Demo Talk.md")
	source := `# Intro

Hello world

%%
Note for intro
comment-properties: text-color=blue
%%

---

# Visual

![[diagram.png]]

%%
	presets: "image-right"
bg: "#f3f0ea"
%%`
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	result, err := buildPresentationFile(rootDir, sourcePath)
	if err != nil {
		t.Fatalf("build presentation: %v", err)
	}

	htmlBytes, err := os.ReadFile(result.PresentationPath)
	if err != nil {
		t.Fatalf("read presentation: %v", err)
	}
	notesBytes, err := os.ReadFile(result.NotesPath)
	if err != nil {
		t.Fatalf("read notes: %v", err)
	}
	metadataBytes, err := os.ReadFile(result.MetadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}

	htmlOutput := string(htmlBytes)
	notesOutput := string(notesBytes)
	metadataOutput := string(metadataBytes)

	if !strings.Contains(htmlOutput, "Demo Talk") {
		t.Fatalf("presentation missing title: %s", htmlOutput)
	}
	if !strings.Contains(htmlOutput, "right-half-image-layout") {
		t.Fatalf("presentation missing image-right preset")
	}
	if !strings.Contains(notesOutput, "Note for intro") {
		t.Fatalf("notes missing speaker note")
	}
	if !strings.Contains(notesOutput, `"notesTextColor":"rgb(26,23,239)"`) {
		t.Fatalf("notes output missing normalized notes text color: %s", notesOutput)
	}
	if !strings.Contains(metadataOutput, `"sourcePath": `) {
		t.Fatalf("metadata missing source path")
	}

	copiedAssetPath := filepath.Join(result.OutputDir, "assets", "diagram.png")
	if _, err := os.Stat(copiedAssetPath); err != nil {
		t.Fatalf("expected copied asset: %v", err)
	}
}

func TestBuildPresentationFileWithCombinedTemplates(t *testing.T) {
	rootDir := t.TempDir()
	sourceDir := filepath.Join(rootDir, "vault")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}

	sourcePath := filepath.Join(sourceDir, "Combined.md")
	source := `# Combined

Body text.

%%
presets: "marked-text, smaller-text"
%%`
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	result, err := buildPresentationFile(rootDir, sourcePath)
	if err != nil {
		t.Fatalf("build presentation: %v", err)
	}

	htmlBytes, err := os.ReadFile(result.PresentationPath)
	if err != nil {
		t.Fatalf("read presentation: %v", err)
	}

	htmlOutput := string(htmlBytes)
	if !strings.Contains(htmlOutput, "preset-marked-text") {
		t.Fatalf("presentation missing marked-text preset class: %s", htmlOutput)
	}
	if !strings.Contains(htmlOutput, "preset-smaller-text") {
		t.Fatalf("presentation missing smaller-text preset class: %s", htmlOutput)
	}
}

func TestParseCommentPropertiesTextColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "hex", input: "text-color=#ffaa00", want: "#ffaa00"},
		{name: "rgb", input: "text-color=rgb(1, 2, 3)", want: "rgb(1, 2, 3)"},
		{name: "named blue override", input: "text-color=blue", want: "rgb(26,23,239)"},
		{name: "other named color", input: "text-color=red", want: "red"},
		{name: "colon separator", input: "text-color: #123456", want: "#123456"},
		{name: "invalid ignored", input: "text-color=not a color", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseCommentProperties(tt.input)
			if got.TextColor != tt.want {
				t.Fatalf("text color = %q, want %q", got.TextColor, tt.want)
			}
		})
	}
}

func TestMarkdownToHTMLConvertsArrowsInVisibleText(t *testing.T) {
	t.Parallel()

	rendered, err := markdownToHTML(`-> left and right <-`)
	if err != nil {
		t.Fatalf("markdown to html: %v", err)
	}
	if !strings.Contains(rendered, "→ left and right ←") {
		t.Fatalf("rendered output missing converted arrows: %s", rendered)
	}

	rendered, err = markdownToHTML("```\n-> <-\n```")
	if err != nil {
		t.Fatalf("markdown to html for code block: %v", err)
	}
	if strings.Contains(rendered, "→") || strings.Contains(rendered, "←") {
		t.Fatalf("code block should not contain converted arrows: %s", rendered)
	}
	if !strings.Contains(rendered, "-&gt; &lt;-") {
		t.Fatalf("code block text should remain unchanged apart from html escaping: %s", rendered)
	}
}
