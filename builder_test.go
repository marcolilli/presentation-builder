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
%%

---

# Visual

![[diagram.png]]

%%
template: "image-right"
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
		t.Fatalf("presentation missing image-right template")
	}
	if !strings.Contains(notesOutput, "Note for intro") {
		t.Fatalf("notes missing speaker note")
	}
	if !strings.Contains(metadataOutput, `"sourcePath": `) {
		t.Fatalf("metadata missing source path")
	}

	copiedAssetPath := filepath.Join(result.OutputDir, "assets", "diagram.png")
	if _, err := os.Stat(copiedAssetPath); err != nil {
		t.Fatalf("expected copied asset: %v", err)
	}
}

