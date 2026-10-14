package mail

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// BodyDiagnostics describes the local MIME-to-text extraction, not whether a
// reader has received every substantive fact in a message. Counts include the
// root entity and every visited MIME part. On a parser error they describe only
// the portion visited before the error. No image is fetched or rendered.
type BodyDiagnostics struct {
	ExtractionError     string            `json:"extractionError,omitempty"`
	PartialFile         bool              `json:"partialFile"`
	MIMEPartCount       int               `json:"mimePartCount"`
	ImagePartCount      int               `json:"imagePartCount"`
	NonTextPartCount    int               `json:"nonTextPartCount"`
	AttachmentPartCount int               `json:"attachmentPartCount"`
	HTMLImageCount      int               `json:"htmlImageCount"`
	HTMLImageAltTexts   []string          `json:"htmlImageAltTexts,omitempty"`
	RichContentElements int               `json:"richContentElements"`
	ExtractedCharCount  int               `json:"extractedCharCount"`
	Alternatives        []BodyAlternative `json:"alternatives,omitempty"`
	Warnings            []string          `json:"warnings,omitempty"`
}

// BodyAlternative records the independently extracted text of a direct child
// of multipart/alternative. Group and Part are MIME tree paths, starting at 1.
// Selected identifies the rendering chosen within that group by the existing
// extractor; in nested groups it does not imply selection by all ancestors.
type BodyAlternative struct {
	Group              string `json:"group"`
	Part               string `json:"part"`
	MediaType          string `json:"mediaType"`
	Selected           bool   `json:"selected"`
	Text               string `json:"text,omitempty"`
	ExtractedCharCount int    `json:"extractedCharCount"`
}

func normalizeBodyText(text string) string {
	return strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
}

func markSelectedAlternative(diagnostics *BodyDiagnostics, index int) {
	if diagnostics != nil && index >= 0 {
		diagnostics.Alternatives[index].Selected = true
	}
}

func (d *BodyDiagnostics) finish() {
	if d.PartialFile {
		d.Warnings = append(d.Warnings, "The local source is a .partial.emlx file.")
	}
	if d.HTMLImageCount > 0 {
		d.Warnings = append(d.Warnings, fmt.Sprintf("HTML contains %d image element(s); image pixels and alt attributes are not included in extracted text.", d.HTMLImageCount))
	}
	if d.RichContentElements > 0 {
		d.Warnings = append(d.Warnings, fmt.Sprintf("HTML contains %d rich-content element(s) or background-image style(s) that are not rendered into extracted text.", d.RichContentElements))
	}
	if d.AttachmentPartCount > 0 {
		d.Warnings = append(d.Warnings, fmt.Sprintf("%d attachment MIME part(s) were skipped by body extraction.", d.AttachmentPartCount))
	}
	if d.NonTextPartCount > 0 {
		d.Warnings = append(d.Warnings, fmt.Sprintf("%d non-text MIME part(s) are not included in extracted text.", d.NonTextPartCount))
	}
	selected := make(map[string]string)
	for _, alternative := range d.Alternatives {
		if alternative.Selected {
			selected[alternative.Group] = alternative.Text
		}
	}
	for _, alternative := range d.Alternatives {
		chosen, exists := selected[alternative.Group]
		if exists && !alternative.Selected && alternative.Text != "" && alternative.Text != chosen {
			d.Warnings = append(d.Warnings, fmt.Sprintf("MIME alternative %s contains different extracted text from selected rendering in group %s; inspect alternatives for its text.", alternative.Part, alternative.Group))
		}
	}
}

// inspectHTML inventories markup without changing the extraction or loading
// referenced resources. Alt text is evidence for callers, never substituted
// into the body. Counts cover HTML from all visited renderings, including
// hidden markup, rather than attempting to guess what an image communicates.
func (d *BodyDiagnostics) inspectHTML(source string) {
	tokenizer := html.NewTokenizer(strings.NewReader(source))
	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := tokenizer.TagName()
			tag := string(name)
			if tag == "img" {
				d.HTMLImageCount++
			}
			switch tag {
			case "svg", "canvas", "video", "audio", "object", "embed", "iframe":
				d.RichContentElements++
			}
			for {
				key, value, more := tokenizer.TagAttr()
				if tag == "img" && string(key) == "alt" && strings.TrimSpace(string(value)) != "" {
					d.HTMLImageAltTexts = append(d.HTMLImageAltTexts, string(value))
				}
				if string(key) == "style" && htmlStyleValue(string(value), "background-image") != "" && htmlStyleValue(string(value), "background-image") != "none" {
					d.RichContentElements++
				}
				if !more {
					break
				}
			}
		}
	}
}
