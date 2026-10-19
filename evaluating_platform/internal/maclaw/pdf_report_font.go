package maclaw

// pdf_report_font.go — the TrueType font embedded in exported report PDFs.
//
// # The defect this fixes (2026-10-19)
//
// The renderer declared `/BaseFont /STSong-Light` with `/Encoding /UniGB-UCS2-H`
// and **no font program** — a CID font identified by a name and expected to be
// present on the reader's machine. Desktop PDF readers with the Adobe Asian font
// pack render it; browsers do not. Chrome's PDFium, Firefox's pdf.js and Safari
// have no STSong-Light, so every Chinese character came out as garbage in the
// one place the report is actually read. Reported from the field after the stage-5
// run: 「生成pdf的时候字体不内嵌导致在浏览器上打开都是乱码」.
//
// # The fix: embed the font, map text through glyph ids
//
// The PDF now carries a CIDFontType2 with a `/FontFile2` stream holding the whole
// font, `/Encoding /Identity-H` (so the two-byte code written for each character
// IS its glyph id), and a `/ToUnicode` CMap so the text stays copyable and
// searchable. Nothing about the layout changed — only how a rune becomes a code
// and how wide it is.
//
// # Why the whole font, not a subset
//
// Subsetting would keep reports small, and it is the obvious next step. It also
// means writing glyf/loca rewriting (including composite glyphs) correctly, and a
// malformed subset produces a PDF that is broken in a *new* way. Correctness
// first: embed everything, measure the size, subset later if it matters.
//
// # Why the font is vendored in the repository
//
// The container has no CJK font, and installing one through the package manager
// ties PDF correctness to an apk name that cannot be verified here. Vendoring
// makes the build reproducible; the licence (Apache-2.0, from the Android Open
// Source Project) is recorded in fonts/LICENSE-DroidSansFallback.txt.

import (
	"bytes"
	"compress/flate"
	_ "embed"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
)

//go:embed fonts/DroidSansFallback.ttf
var embeddedReportFontTTF []byte

// pdfReportFont is parsed once at init. A malformed embedded asset is a build
// error rather than a runtime condition, so init panics — the same rule this
// package already applies to its generated catalogues.
var reportPDFFont = mustLoadPDFReportFont(embeddedReportFontTTF)

type pdfReportFont struct {
	data       []byte
	unitsPerEm uint16
	numGlyphs  uint16
	advances   []uint16 // per glyph id, in font units
	// cidWidths maps a glyph id to its advance in PDF text space (1000 per em).
	cidWidths map[uint16]int

	unicodeToGID map[rune]uint16
	gidToUnicode map[uint16]rune

	bboxXMin, bboxYMin, bboxXMax, bboxYMax int16
	ascent, descent                        int16
}

func mustLoadPDFReportFont(data []byte) *pdfReportFont {
	font, err := loadPDFReportFont(data)
	if err != nil {
		panic(fmt.Sprintf("maclaw: embedded report font is unusable: %v", err))
	}
	return font
}

func loadPDFReportFont(data []byte) (*pdfReportFont, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("font data is %d bytes, too short for an sfnt header", len(data))
	}
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	if len(data) < 12+numTables*16 {
		return nil, fmt.Errorf("sfnt table directory is truncated")
	}
	tables := make(map[string][]byte, numTables)
	for i := 0; i < numTables; i++ {
		rec := data[12+i*16 : 12+(i+1)*16]
		tag := string(rec[0:4])
		offset := int(binary.BigEndian.Uint32(rec[8:12]))
		length := int(binary.BigEndian.Uint32(rec[12:16]))
		if offset < 0 || length < 0 || offset+length > len(data) {
			return nil, fmt.Errorf("sfnt table %q points outside the font (offset=%d len=%d)", tag, offset, length)
		}
		tables[tag] = data[offset : offset+length]
	}

	head, ok := tables["head"]
	if !ok || len(head) < 54 {
		return nil, fmt.Errorf("font has no usable head table")
	}
	maxp, ok := tables["maxp"]
	if !ok || len(maxp) < 6 {
		return nil, fmt.Errorf("font has no usable maxp table")
	}
	hhea, ok := tables["hhea"]
	if !ok || len(hhea) < 36 {
		return nil, fmt.Errorf("font has no usable hhea table")
	}
	hmtx, ok := tables["hmtx"]
	if !ok {
		return nil, fmt.Errorf("font has no hmtx table")
	}
	cmap, ok := tables["cmap"]
	if !ok {
		return nil, fmt.Errorf("font has no cmap table")
	}

	font := &pdfReportFont{
		data:         data,
		unitsPerEm:   binary.BigEndian.Uint16(head[18:20]),
		numGlyphs:    binary.BigEndian.Uint16(maxp[4:6]),
		ascent:       int16(binary.BigEndian.Uint16(hhea[4:6])),
		descent:      int16(binary.BigEndian.Uint16(hhea[6:8])),
		bboxXMin:     int16(binary.BigEndian.Uint16(head[36:38])),
		bboxYMin:     int16(binary.BigEndian.Uint16(head[38:40])),
		bboxXMax:     int16(binary.BigEndian.Uint16(head[40:42])),
		bboxYMax:     int16(binary.BigEndian.Uint16(head[42:44])),
		cidWidths:    map[uint16]int{},
		unicodeToGID: map[rune]uint16{},
		gidToUnicode: map[uint16]rune{},
	}
	if font.unitsPerEm == 0 {
		return nil, fmt.Errorf("font declares unitsPerEm=0")
	}

	font.advances = parseAdvanceWidths(hmtx, hhea, font.numGlyphs)
	if err := parseUnicodeCMap(cmap, font); err != nil {
		return nil, err
	}
	if len(font.unicodeToGID) == 0 {
		return nil, fmt.Errorf("font cmap maps no Unicode code points")
	}
	return font, nil
}

// parseAdvanceWidths reads hmtx. Glyphs past numberOfHMetrics share the last
// advance, which is the format's own rule — not a fallback.
func parseAdvanceWidths(hmtx, hhea []byte, numGlyphs uint16) []uint16 {
	numberOfHMetrics := int(binary.BigEndian.Uint16(hhea[34:36]))
	advances := make([]uint16, numGlyphs)
	last := uint16(0)
	for gid := 0; gid < int(numGlyphs); gid++ {
		if gid < numberOfHMetrics {
			offset := gid * 4
			if offset+2 > len(hmtx) {
				break
			}
			last = binary.BigEndian.Uint16(hmtx[offset : offset+2])
		}
		advances[gid] = last
	}
	return advances
}

// parseUnicodeCMap fills unicodeToGID/gidToUnicode from the best available
// subtable: full-repertoire format 12 first, then the BMP format 4.
//
// Later duplicates are ignored rather than overwritten: several code points can
// map to one glyph (a composed and a precomposed form), and the ToUnicode stream
// has to pick one deterministically.
func parseUnicodeCMap(cmap []byte, font *pdfReportFont) error {
	if len(cmap) < 4 {
		return fmt.Errorf("cmap table is truncated")
	}
	numSubtables := int(binary.BigEndian.Uint16(cmap[2:4]))
	type candidate struct {
		offset   int
		fullRep  bool
		priority int
	}
	var candidates []candidate
	for i := 0; i < numSubtables; i++ {
		rec := 4 + i*8
		if rec+8 > len(cmap) {
			break
		}
		platformID := binary.BigEndian.Uint16(cmap[rec : rec+2])
		encodingID := binary.BigEndian.Uint16(cmap[rec+2 : rec+4])
		offset := int(binary.BigEndian.Uint32(cmap[rec+4 : rec+8]))
		if offset < 0 || offset+2 > len(cmap) {
			continue
		}
		format := binary.BigEndian.Uint16(cmap[offset : offset+2])
		if format != 4 && format != 12 {
			continue
		}
		priority := 100
		switch {
		case platformID == 3 && encodingID == 10: // Windows UCS-4
			priority = 0
		case platformID == 0: // Unicode
			priority = 1
		case platformID == 3 && encodingID == 1: // Windows BMP
			priority = 2
		}
		candidates = append(candidates, candidate{offset: offset, fullRep: format == 12, priority: priority})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].priority < candidates[j].priority })

	// Every usable subtable is merged, best first, rather than the first one being
	// taken and the rest ignored.
	//
	// That is not defensive padding: DroidSansFallback's format-12 subtable covers
	// the CJK repertoire but **not ASCII**, while its format-4 subtable carries
	// the Latin range. Stopping at the first table that parsed produced a font map
	// with 33k entries and no `A` — every Latin character in a report became
	// .notdef, which is a box, and the layout silently used the default advance.
	//
	// `record` keeps the first mapping it sees for each code, so priority order
	// still decides which table wins where they overlap.
	for _, c := range candidates {
		if c.fullRep {
			parseCMapFormat12(cmap[c.offset:], font)
			continue
		}
		parseCMapFormat4(cmap[c.offset:], font)
	}
	if len(font.unicodeToGID) == 0 {
		return fmt.Errorf("no usable format 4 or 12 cmap subtable")
	}
	return nil
}

func (f *pdfReportFont) record(code rune, gid uint16) {
	if gid == 0 {
		return
	}
	if _, exists := f.unicodeToGID[code]; !exists {
		f.unicodeToGID[code] = gid
	}
	if _, exists := f.gidToUnicode[gid]; !exists {
		f.gidToUnicode[gid] = code
	}
}

func parseCMapFormat12(table []byte, font *pdfReportFont) bool {
	if len(table) < 16 {
		return false
	}
	nGroups := int(binary.BigEndian.Uint32(table[12:16]))
	if nGroups <= 0 || 16+nGroups*12 > len(table) {
		return false
	}
	for i := 0; i < nGroups; i++ {
		group := table[16+i*12 : 16+(i+1)*12]
		start := binary.BigEndian.Uint32(group[0:4])
		end := binary.BigEndian.Uint32(group[4:8])
		startGlyph := binary.BigEndian.Uint32(group[8:12])
		if end < start || end > 0x10FFFF {
			continue
		}
		for code := start; code <= end; code++ {
			gid := startGlyph + (code - start)
			if gid > uint32(font.numGlyphs) {
				break
			}
			font.record(rune(code), uint16(gid))
		}
	}
	return len(font.unicodeToGID) > 0
}

func parseCMapFormat4(table []byte, font *pdfReportFont) bool {
	if len(table) < 14 {
		return false
	}
	segCountX2 := int(binary.BigEndian.Uint16(table[6:8]))
	segCount := segCountX2 / 2
	if segCount == 0 {
		return false
	}
	endCodes := 14
	startCodes := endCodes + segCountX2 + 2 // skip reservedPad
	idDeltas := startCodes + segCountX2
	idRangeOffsets := idDeltas + segCountX2
	if idRangeOffsets+segCountX2 > len(table) {
		return false
	}
	for seg := 0; seg < segCount; seg++ {
		end := binary.BigEndian.Uint16(table[endCodes+seg*2 : endCodes+seg*2+2])
		start := binary.BigEndian.Uint16(table[startCodes+seg*2 : startCodes+seg*2+2])
		delta := int16(binary.BigEndian.Uint16(table[idDeltas+seg*2 : idDeltas+seg*2+2]))
		rangeOffsetAt := idRangeOffsets + seg*2
		rangeOffset := binary.BigEndian.Uint16(table[rangeOffsetAt : rangeOffsetAt+2])
		if start > end || start == 0xFFFF {
			continue
		}
		for code := start; code <= end && code != 0xFFFF; code++ {
			var gid uint16
			if rangeOffset == 0 {
				gid = uint16(int32(code) + int32(delta))
			} else {
				// The offset is relative to the location of the idRangeOffset
				// entry itself — the classic gnarliest detail of format 4.
				index := int(rangeOffsetAt) + int(rangeOffset) + int(code-start)*2
				if index+2 > len(table) {
					continue
				}
				raw := binary.BigEndian.Uint16(table[index : index+2])
				if raw == 0 {
					continue
				}
				gid = uint16(int32(raw) + int32(delta))
			}
			font.record(rune(code), gid)
		}
	}
	return len(font.unicodeToGID) > 0
}

// GlyphID resolves a rune to its glyph id, or 0 (the font's .notdef) when the
// font has no glyph for it. Falling back to .notdef rather than dropping the
// character keeps the text length and the ToUnicode mapping consistent; the
// reader sees a hollow box, which is the honest "this font lacks the glyph".
func (f *pdfReportFont) GlyphID(r rune) uint16 {
	if gid, ok := f.unicodeToGID[r]; ok {
		return gid
	}
	return 0
}

// advancePoints is the advance width of one glyph at a given font size, in
// points. This is what the layout uses for line breaking and for advancing the
// text cursor, replacing the old hand-written per-rune table.
func (f *pdfReportFont) advancePoints(r rune, size float64) float64 {
	gid := f.GlyphID(r)
	if int(gid) >= len(f.advances) {
		return size
	}
	return float64(f.advances[gid]) * size / float64(f.unitsPerEm)
}

// cidWidth is the glyph's advance in PDF text space (1000 units per em), the
// unit the /W array is expressed in.
func (f *pdfReportFont) cidWidth(gid uint16) int {
	if cached, ok := f.cidWidths[gid]; ok {
		return cached
	}
	advance := uint16(0)
	if int(gid) < len(f.advances) {
		advance = f.advances[gid]
	}
	width := int(float64(advance) * 1000 / float64(f.unitsPerEm))
	f.cidWidths[gid] = width
	return width
}

// encodeText turns a string into Identity-H text: four hex digits per glyph id.
func (f *pdfReportFont) encodeText(value string) string {
	if value == "" {
		return ""
	}
	var buf strings.Builder
	buf.Grow(len([]rune(value)) * 4)
	for _, r := range value {
		fmt.Fprintf(&buf, "%04X", f.GlyphID(r))
	}
	return buf.String()
}

// textWidth is the advance of a whole string at a given size, in points.
func (f *pdfReportFont) textWidth(value string, size float64) float64 {
	width := 0.0
	for _, r := range []rune(value) {
		width += f.advancePoints(r, size)
	}
	return width
}

// ─────────────────────────────────────────────────────────────────────────────
// PDF objects
// ─────────────────────────────────────────────────────────────────────────────

// pdfType0FontObject is the text font the page resources name (/F1).
//
// `/Encoding /Identity-H` is what makes the two-byte code written for a rune its
// glyph id directly — no CID-to-GID table needed, and no dependence on any
// encoding the reader has to know about.
func pdfType0FontObject(fontObj, cidFontObj, toUnicodeObj int) string {
	return fmt.Sprintf(
		"<< /Type /Font /Subtype /Type0 /BaseFont /DroidSansFallback /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>",
		cidFontObj, toUnicodeObj,
	)
}

// pdfCIDFontObject is the descendant CIDFontType2, carrying the embedded program
// and the width table for exactly the glyphs the document uses.
func pdfCIDFontObject(baseFontObj, descriptorObj int, used []uint16) string {
	return fmt.Sprintf(
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /DroidSansFallback "+
			"/CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> "+
			"/FontDescriptor %d 0 R /CIDToGIDMap /Identity /DW 1000 /W [%s] >>",
		descriptorObj, pdfWidthArray(used),
	)
}

// pdfWidthArray renders /W as `gid [width] gid [width] …` for the used glyphs.
//
// Only used glyphs are listed. The default width (/DW 1000) covers everything
// else, so an unlisted glyph still advances rather than stacking on the previous
// one — the failure mode of a /W array that is missing entries.
func pdfWidthArray(used []uint16) string {
	parts := make([]string, 0, len(used))
	for _, gid := range used {
		parts = append(parts, fmt.Sprintf("%d [%d]", gid, reportPDFFont.cidWidth(gid)))
	}
	return strings.Join(parts, " ")
}

func pdfFontDescriptorObject(fontFileObj int) string {
	font := reportPDFFont
	return fmt.Sprintf(
		"<< /Type /FontDescriptor /FontName /DroidSansFallback /Flags 4 "+
			"/FontBBox [%d %d %d %d] /ItalicAngle 0 /Ascent %d /Descent %d /CapHeight %d /StemV 80 "+
			"/FontFile2 %d 0 R >>",
		font.bboxXMin, font.bboxYMin, font.bboxXMax, font.bboxYMax,
		int(font.ascent)*1000/int(font.unitsPerEm),
		int(font.descent)*1000/int(font.unitsPerEm),
		int(font.ascent)*1000/int(font.unitsPerEm),
		fontFileObj,
	)
}

// pdfFontFileObject embeds the whole font program, compressed.
//
// `/Length1` must be the UNCOMPRESSED size or readers refuse the stream — it is
// the declared length of the TrueType program, not of the compressed bytes.
func pdfFontFileObject() string {
	compressed := pdfFlateCompress(reportPDFFont.data)
	return fmt.Sprintf(
		"<< /Length %d /Length1 %d /Filter /FlateDecode >>\nstream\n%s\nendstream",
		len(compressed), len(reportPDFFont.data), string(compressed),
	)
}

// pdfToUnicodeObject maps every used glyph back to its code point.
//
// Without it the text renders correctly but copies out as garbage, and search
// fails — the report is meant to be read *and* quoted from.
func pdfToUnicodeObject(used []uint16) string {
	entries := make([]string, 0, len(used))
	for _, gid := range used {
		code, ok := reportPDFFont.gidToUnicode[gid]
		if !ok || code == 0 {
			continue
		}
		encoded := utf16HexOf(code)
		entries = append(entries, fmt.Sprintf("<%04X> <%s>", gid, encoded))
	}
	body := strings.Join(entries, "\n")
	stream := "/CIDInit /ProcSet findresource begin\n" +
		"12 dict begin\nbegincmap\n/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n" +
		"/CMapName /Adobe-Identity-UCS def\n/CMapType 2 def\n" +
		"1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n" +
		fmt.Sprintf("%d beginbfchar\n%s\nendbfchar\n", len(entries), body) +
		"endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n"
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)
}

// utf16HexOf renders one code point as UTF-16BE hex (two code units above the BMP).
func utf16HexOf(code rune) string {
	if code <= 0xFFFF {
		return fmt.Sprintf("%04X", code)
	}
	value := uint32(code) - 0x10000
	high := uint16(0xD800 + (value >> 10))
	low := uint16(0xDC00 + (value & 0x3FF))
	return fmt.Sprintf("%04X%04X", high, low)
}

// collectPDFUsedGlyphs scans the generated content streams for the hex strings a
// `Tj` operator shows and returns the distinct glyph ids in ascending order.
//
// Reading them back out of the content is deliberate: the width table and the
// ToUnicode CMap are then derived from what was actually written, so they cannot
// drift from the text the way a second calculation could.
func collectPDFUsedGlyphs(pages []pdfRenderedPage) []uint16 {
	seen := map[uint16]bool{}
	for _, page := range pages {
		for _, gid := range page.UsedGlyphs {
			seen[gid] = true
		}
	}
	out := make([]uint16, 0, len(seen))
	for gid := range seen {
		out = append(out, gid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// pdfFlateCompress compresses a stream with the same filter the image objects use.
func pdfFlateCompress(data []byte) []byte {
	var buf bytes.Buffer
	writer, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		// flate only fails on an invalid level, which is a constant here.
		return data
	}
	if _, err := writer.Write(data); err != nil {
		return data
	}
	if err := writer.Close(); err != nil {
		return data
	}
	return buf.Bytes()
}
