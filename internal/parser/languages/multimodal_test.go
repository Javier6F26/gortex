package languages

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/zzet/gortex/internal/graph"
)

func TestImageAssetExtractor_PNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 7, 3))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	e := NewImageAssetExtractor()
	result, err := e.Extract("assets/logo.png", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	var imgNode *graph.Node
	for _, n := range result.Nodes {
		if n.Kind == graph.KindImage {
			imgNode = n
		}
	}
	if imgNode == nil {
		t.Fatal("no KindImage node emitted for a PNG")
	}
	if imgNode.Meta["format"] != "png" {
		t.Errorf("format = %v, want png", imgNode.Meta["format"])
	}
	if imgNode.Meta["width"] != 7 || imgNode.Meta["height"] != 3 {
		t.Errorf("dims = %vx%v, want 7x3", imgNode.Meta["width"], imgNode.Meta["height"])
	}
	if imgNode.Meta["asset_kind"] != "image" {
		t.Errorf("asset_kind = %v, want image", imgNode.Meta["asset_kind"])
	}
	if _, ok := imgNode.Meta["sha256"].(string); !ok {
		t.Error("sha256 not stamped")
	}
	// The file node + EdgeDefines wiring.
	var file *graph.Node
	for _, n := range result.Nodes {
		if n.Kind == graph.KindFile {
			file = n
		}
	}
	if file == nil || file.ID != "assets/logo.png" {
		t.Fatalf("file node missing/wrong: %+v", file)
	}
	var defines bool
	for _, ed := range result.Edges {
		if ed.Kind == graph.EdgeDefines && ed.From == file.ID && ed.To == imgNode.ID {
			defines = true
		}
	}
	if !defines {
		t.Error("file -> image EdgeDefines missing")
	}
}

func TestImageAssetExtractor_SVG(t *testing.T) {
	src := []byte(`<?xml version="1.0"?>
<svg width="120" height="48" viewBox="0 0 240 96" xmlns="http://www.w3.org/2000/svg">
  <rect width="240" height="96"/>
</svg>`)
	e := NewImageAssetExtractor()
	result, err := e.Extract("icon.svg", src)
	if err != nil {
		t.Fatal(err)
	}
	var imgNode *graph.Node
	for _, n := range result.Nodes {
		if n.Kind == graph.KindImage {
			imgNode = n
		}
	}
	if imgNode == nil {
		t.Fatal("no KindImage node for SVG")
	}
	if imgNode.Meta["format"] != "svg" {
		t.Errorf("format = %v, want svg", imgNode.Meta["format"])
	}
	if imgNode.Meta["width"] != 120 || imgNode.Meta["height"] != 48 {
		t.Errorf("svg dims = %vx%v, want 120x48", imgNode.Meta["width"], imgNode.Meta["height"])
	}
}

func TestPDFExtractor_GracefulOnGarbage(t *testing.T) {
	e := NewPDFExtractor()
	// Malformed bytes must not panic and must still ingest the file node.
	result, err := e.Extract("spec.pdf", []byte("%PDF-1.4 not really a pdf"))
	if err != nil {
		t.Fatal(err)
	}
	var file *graph.Node
	for _, n := range result.Nodes {
		if n.Kind == graph.KindFile {
			file = n
		}
	}
	if file == nil {
		t.Fatal("PDF file node missing")
	}
	if file.Meta["asset_kind"] != "pdf" {
		t.Errorf("asset_kind = %v, want pdf", file.Meta["asset_kind"])
	}
}
