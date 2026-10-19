package maclaw

import (
	"strings"
	"testing"
)

// TestEmbeddedReportFontCoversCJK pins the parser against the vendored font.
//
// The numbers are not decoration: an earlier version of the parser stopped at the
// first cmap subtable that parsed, and DroidSansFallback's format-12 table carries
// the CJK repertoire while its format-4 table carries the BMP — including the
// ranges the first pass silently skipped. Merging them is what makes the coverage
// below hold.
func TestEmbeddedReportFontCoversCJK(t *testing.T) {
	font := reportPDFFont
	if font.unitsPerEm == 0 {
		t.Fatal("unitsPerEm is zero")
	}
	if len(font.unicodeToGID) < 20000 {
		t.Fatalf("font maps only %d code points; the CJK repertoire is missing", len(font.unicodeToGID))
	}
	for _, r := range "大模型安全评估报告中攻击载荷目标回答判定理由第一轮" {
		if font.GlyphID(r) == 0 {
			t.Errorf("rune %q has no glyph in the embedded font", r)
		}
	}
	if got := font.GlyphID('中'); font.gidToUnicode[got] != '中' {
		t.Errorf("glyph %d maps back to %q, want 中", got, font.gidToUnicode[got])
	}
}

// TestEmbeddedReportFontIsCJKOnly documents a fact the renderer depends on.
//
// DroidSansFallback is a *fallback* face: it has no Latin glyphs. That is why the
// PDF keeps Helvetica for ASCII — routing Latin through this font would render
// every English character as .notdef.
func TestEmbeddedReportFontIsCJKOnly(t *testing.T) {
	if gid := reportPDFFont.GlyphID('A'); gid != 0 {
		t.Fatalf("font maps 'A' to %d; if it gains Latin glyphs the ASCII/Helvetica split can be revisited", gid)
	}
}

// TestReportPDFDeclaresBothFonts pins the resource entry.
//
// A font object that exists but is not listed in the page's /Resources /Font dict
// is invisible: the content stream's `/F2 … Tf` resolves to nothing and the text
// is dropped. That happened during this change — the Helvetica object was built
// and the page never named it.
func TestReportPDFDeclaresBothFonts(t *testing.T) {
	pdf := string(renderReportPDF(&EvaluationReport{ID: "r", Title: "报告", Findings: []EvaluationReportFinding{{
		Title: "载荷 final English prompt", Description: "回答", Severity: "high",
	}}}))
	for _, want := range []string{"/F1 3 0 R", "/F2 8 0 R", "/FontFile2", "/Identity-H", "/ToUnicode"} {
		if !strings.Contains(pdf, want) {
			t.Errorf("pdf is missing %s", want)
		}
	}
	if strings.Contains(pdf, "/STSong-Light") {
		t.Error("pdf still references the non-embedded STSong-Light font")
	}
	if !pdfContainsRenderedText(pdf, "载荷") {
		t.Error("CJK text was not rendered through the embedded font")
	}
	if !pdfContainsRenderedText(pdf, "final English prompt") {
		t.Error("Latin text was not rendered through Helvetica")
	}
}

// TestEmbeddedFontToUnicodeCoversEveryUsedGlyph is the "text stays copyable" check.
//
// Without a ToUnicode entry a glyph still draws, but copying it out of the PDF
// yields the raw Identity-H code — the report would look right and be impossible
// to quote.
func TestEmbeddedFontToUnicodeCoversEveryUsedGlyph(t *testing.T) {
	report := &EvaluationReport{ID: "r", Title: "安全评估报告", Findings: []EvaluationReportFinding{{
		Title: "攻击载荷", Description: "目标回答", Suggestion: "判定理由", Severity: "high",
	}}}
	pages := []pdfRenderedPage{pdfCoverPageContent(report)}
	pages = append(pages, pdfBodyPageContents(report)...)
	used := collectPDFUsedGlyphs(pages)
	if len(used) == 0 {
		t.Fatal("no glyphs recorded; the page writers are not tracking usage")
	}
	cmap := pdfToUnicodeObject(used)
	for _, gid := range used {
		if !strings.Contains(cmap, "<"+strings.ToUpper(hex4(gid))+">") {
			t.Fatalf("ToUnicode is missing glyph %d", gid)
		}
	}
}

// hex4 renders a glyph id the way the PDF writes it.
func hex4(gid uint16) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{
		digits[(gid>>12)&0xF], digits[(gid>>8)&0xF], digits[(gid>>4)&0xF], digits[gid&0xF],
	})
}
