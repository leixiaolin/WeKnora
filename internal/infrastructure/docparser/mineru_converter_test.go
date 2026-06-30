package docparser

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestNormalizeMinerUMarkdownPreservesMarkdownAndHTML(t *testing.T) {
	input := strings.Join([]string{
		"# Heading",
		"",
		"![](images/cover.jpg)",
		"",
		`<details><summary>text_image</summary>caption</details>`,
		"",
		`<table><tr><td><img src="images/profile.jpg"/></td></tr></table>`,
	}, "\n")

	got := normalizeMinerUMarkdown(input)

	if !strings.Contains(got, "# Heading") {
		t.Fatalf("expected heading to stay intact, got: %q", got)
	}
	if strings.Contains(got, `\# Heading`) {
		t.Fatalf("expected heading to avoid escaped form, got: %q", got)
	}
	if !strings.Contains(got, "![](images/cover.jpg)") {
		t.Fatalf("expected markdown image syntax to stay intact, got: %q", got)
	}
	if strings.Contains(got, `!\[](images/cover.jpg)`) {
		t.Fatalf("expected markdown image syntax to avoid escaped form, got: %q", got)
	}
	if !strings.Contains(got, `<details><summary>text_image</summary>caption</details>`) {
		t.Fatalf("expected details/summary block to be preserved, got: %q", got)
	}
	if !strings.Contains(got, `<img src="images/profile.jpg"/>`) {
		t.Fatalf("expected html img tag to be preserved, got: %q", got)
	}
}

func TestProcessImagesKeepsReferencedVariants(t *testing.T) {
	reader := &MinerUReader{}
	mdContent := strings.Join([]string{
		"![](images/cover.jpg)",
		`<img src="./images/profile.jpg"/>`,
		`![](plain.jpg)`,
	}, "\n")

	png := createTestPNG(200, 150)
	b64 := base64.StdEncoding.EncodeToString(png)
	images := map[string]string{
		"cover.jpg":   "data:image/png;base64," + b64,
		"profile.jpg": "data:image/png;base64," + b64,
		"plain.jpg":   "data:image/png;base64," + b64,
	}

	refs, gotMarkdown := reader.processImages(mdContent, images)

	if gotMarkdown != mdContent {
		t.Fatalf("processImages should not rewrite markdown content")
	}
	if len(refs) != 3 {
		t.Fatalf("expected 3 image refs, got %d", len(refs))
	}
}

func TestProcessImagesMatchesPathsWithSpaces(t *testing.T) {
	reader := &MinerUReader{}
	mdContent := "![](images/page 1.jpg)"

	png := createTestPNG(200, 150)
	b64 := base64.StdEncoding.EncodeToString(png)
	images := map[string]string{
		"page 1.jpg": "data:image/png;base64," + b64,
	}

	refs, _ := reader.processImages(mdContent, images)
	if len(refs) != 1 {
		t.Fatalf("expected 1 image ref for path with spaces, got %d", len(refs))
	}
	if refs[0].OriginalRef != "images/page 1.jpg" {
		t.Fatalf("unexpected OriginalRef: %q", refs[0].OriginalRef)
	}
}

func TestProcessImagesMatchesUnicodePaths(t *testing.T) {
	reader := &MinerUReader{}
	mdContent := "![](images/第1页 图表.png)"

	png := createTestPNG(200, 150)
	b64 := base64.StdEncoding.EncodeToString(png)
	images := map[string]string{
		"第1页 图表.png": "data:image/png;base64," + b64,
	}

	refs, _ := reader.processImages(mdContent, images)
	if len(refs) != 1 {
		t.Fatalf("expected 1 image ref for unicode path, got %d", len(refs))
	}
	if refs[0].OriginalRef != "images/第1页 图表.png" {
		t.Fatalf("unexpected OriginalRef: %q", refs[0].OriginalRef)
	}
}

func TestMinerUReadRequiresEndpoint(t *testing.T) {
	reader := NewMinerUReader(nil)

	result, err := reader.Read(context.Background(), &types.ReadRequest{
		FileName:    "sample.pdf",
		FileType:    "pdf",
		FileContent: []byte("%PDF-1.7"),
	})

	if err != nil {
		t.Fatalf("Read returned unexpected error: %v", err)
	}
	if result == nil || !strings.Contains(result.Error, "MinerU endpoint is not configured") {
		t.Fatalf("expected diagnostic endpoint error, got %#v", result)
	}
}

func TestMinerUCloudReadRequiresAPIKey(t *testing.T) {
	reader := NewMinerUCloudReader(nil)

	result, err := reader.Read(context.Background(), &types.ReadRequest{
		FileName:    "sample.pdf",
		FileType:    "pdf",
		FileContent: []byte("%PDF-1.7"),
	})

	if err != nil {
		t.Fatalf("Read returned unexpected error: %v", err)
	}
	if result == nil || !strings.Contains(result.Error, "MinerU Cloud API key is not configured") {
		t.Fatalf("expected diagnostic API key error, got %#v", result)
	}
}

func TestExtractMinerUFileParseResultSupportsNamedResults(t *testing.T) {
	body := []byte(`{
		"results": {
			"sample.pdf": {
				"md_content": "# Title\n\n![](images/page 1.png)",
				"images": {
					"images/page 1.png": "data:image/png;base64,AA=="
				}
			}
		}
	}`)

	md, images, source, err := extractMinerUFileParseResult(body)
	if err != nil {
		t.Fatalf("extractMinerUFileParseResult returned error: %v", err)
	}
	if md != "# Title\n\n![](images/page 1.png)" {
		t.Fatalf("unexpected markdown: %q", md)
	}
	if images["images/page 1.png"] != "data:image/png;base64,AA==" {
		t.Fatalf("unexpected images: %#v", images)
	}
	if source != "results.sample.pdf" {
		t.Fatalf("unexpected source: %s", source)
	}
}

func TestExtractMinerUFileParseResultFailsOnEmptyResult(t *testing.T) {
	body := []byte(`{"results":{"document":{}}}`)

	_, _, _, err := extractMinerUFileParseResult(body)
	if err == nil || !strings.Contains(err.Error(), "no markdown/images") {
		t.Fatalf("expected empty result error, got %v", err)
	}
}
