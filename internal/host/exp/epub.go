package exp

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

// renderEPUB packs a set of chapters into an EPUB 3 byte stream.
//
// Package layout (OEBPS is the OPS package container):
//
//	mimetype                    (must be the first zip entry with Method=Store, uncompressed)
//	META-INF/container.xml      (points at OEBPS/content.opf)
//	OEBPS/content.opf           （metadata + manifest + spine）
//	OEBPS/nav.xhtml             （EPUB 3 navigation）
//	OEBPS/style.css             (minimal typography)
//	OEBPS/cover.xhtml           (book title, optional)
//	OEBPS/chapterNNN.xhtml      (one file per chapter)
// epubLang returns the BCP 47 tag written into the EPUB. The pipeline is Vietnamese-only, so
// this is the product default; a novel declared with the wrong language tag would make readers
// render it with the wrong typography.
func epubLang() string { return "vi" }

func renderEPUB(
	book domain.BookMetadata,
	chapters []int,
	titleIdx chapterTitleIndex,
	locations map[int]chapterLocation,
	bodies map[int]string,
	lang string,
) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// 1. mimetype must be the first zip entry with Store (uncompressed) and content exactly without a BOM.
	mt, err := zw.CreateHeader(&zip.FileHeader{
		Name:   "mimetype",
		Method: zip.Store,
	})
	if err != nil {
		return nil, fmt.Errorf("create mimetype: %w", err)
	}
	if _, err := mt.Write([]byte("application/epub+zip")); err != nil {
		return nil, err
	}

	if err := zipDeflate(zw, "META-INF/container.xml", containerXML); err != nil {
		return nil, err
	}
	if err := zipDeflate(zw, "OEBPS/style.css", styleCSS); err != nil {
		return nil, err
	}

	hasCover := strings.TrimSpace(book.Title) != ""
	if hasCover {
		if err := zipDeflate(zw, "OEBPS/cover.xhtml", renderCoverXHTML(book.Title, lang)); err != nil {
			return nil, err
		}
	}

	for _, ch := range chapters {
		loc, hasLoc := locations[ch]
		title := strings.TrimSpace(titleIdx[ch])
		body := stripChapterTitleHeader(strings.TrimSpace(bodies[ch]), title)
		xhtml := renderChapterXHTML(ch, title, loc, hasLoc, body, lang)
		if err := zipDeflate(zw, "OEBPS/"+chapterFileName(ch), xhtml); err != nil {
			return nil, err
		}
	}

	if err := zipDeflate(zw, "OEBPS/nav.xhtml", renderNavXHTML(hasCover, chapters, titleIdx, lang)); err != nil {
		return nil, err
	}

	if err := zipDeflate(zw, "OEBPS/content.opf", renderOPF(book, hasCover, chapters, lang)); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finalize zip: %w", err)
	}
	return buf.Bytes(), nil
}

// zipDeflate writes an ordinary (compressed) entry.
func zipDeflate(zw *zip.Writer, name, content string) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	_, err = w.Write([]byte(content))
	return err
}

func chapterFileName(ch int) string {
	return fmt.Sprintf("chapter%03d.xhtml", ch)
}

// chapterID is the manifest item id; it corresponds one-to-one with the file name.
func chapterID(ch int) string {
	return fmt.Sprintf("ch%03d", ch)
}

// Fixed templates ----------------------------------------

const containerXML = `<?xml version="1.0" encoding="utf-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>
`

const styleCSS = `body { font-family: serif; line-height: 1.7; margin: 1em; }
h1.book-title { font-size: 2em; text-align: center; margin: 4em 0 1em; }
.volume-divider { font-size: 1.6em; text-align: center; margin: 4em 0 1em; font-weight: bold; }
h1.chapter-title { font-size: 1.4em; text-align: center; margin: 2em 0 1.5em; }
p { text-indent: 2em; margin: 0.5em 0; }
`

// Chapter XHTML ------------------------------------------

func renderChapterXHTML(ch int, title string, loc chapterLocation, hasLoc bool, body string, lang string) string {
	var b strings.Builder
	displayTitle := fmt.Sprintf("Chương %d", ch)
	if title != "" {
		displayTitle = fmt.Sprintf("Chương %d %s", ch, title)
	}

	fmt.Fprintf(&b, `<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="%[1]s">
<head>
  <title>%s</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
</head>
<body>
`, lang, html.EscapeString(displayTitle))

	if hasLoc && loc.IsFirstOfVolume {
		fmt.Fprintf(&b, "  <div class=\"volume-divider\">Tập %d %s</div>\n",
			loc.VolumeIdx, html.EscapeString(strings.TrimSpace(loc.VolumeTitle)))
	}

	fmt.Fprintf(&b, "  <h1 class=\"chapter-title\">%s</h1>\n", html.EscapeString(displayTitle))
	for _, para := range splitParagraphs(body) {
		fmt.Fprintf(&b, "  <p>%s</p>\n", html.EscapeString(para))
	}
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// splitParagraphs splits on blank lines, treating several consecutive blanks as one separator.
// The returned paragraphs are all trimmed and non-empty.
// A newline inside a paragraph (a single \n) is kept as an intra-paragraph space — XHTML <p>
// does not preserve newlines and the browser wraps automatically.
func splitParagraphs(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	parts := strings.Split(body, "\n\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Intra-paragraph newlines become spaces so XHTML rendering loses no content.
		p = strings.ReplaceAll(p, "\n", " ")
		out = append(out, p)
	}
	return out
}

// Cover ------------------------------------------------

func renderCoverXHTML(novelName, lang string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="%[1]s">
<head>
  <title>Bìa</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
</head>
<body>
`, lang))
	if name := strings.TrimSpace(novelName); name != "" {
		fmt.Fprintf(&b, "  <h1 class=\"book-title\">%s</h1>\n", html.EscapeString(name))
	}
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// nav.xhtml（EPUB 3 navigation）────────────────────────────────────────────────

func renderNavXHTML(hasCover bool, chapters []int, titleIdx chapterTitleIndex, lang string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" xml:lang="%[1]s">
<head>
  <title>Mục lục</title>
  <link rel="stylesheet" type="text/css" href="style.css"/>
</head>
<body>
  <nav epub:type="toc">
    <h1>Mục lục</h1>
    <ol>
`, lang))
	if hasCover {
		b.WriteString("      <li><a href=\"cover.xhtml\">Bìa</a></li>\n")
	}

	// Flattened chapter list. Grouping by volume/arc reads worse in a reader than a single-level
	// table of contents (readers collapse it themselves anyway), and a nested ol in an EPUB 3 nav
	// renders oddly in some readers. Keep it simple.
	for _, ch := range chapters {
		title := strings.TrimSpace(titleIdx[ch])
		display := fmt.Sprintf("Chương %d", ch)
		if title != "" {
			display = fmt.Sprintf("Chương %d %s", ch, title)
		}
		fmt.Fprintf(&b, "      <li><a href=\"%s\">%s</a></li>\n",
			chapterFileName(ch), html.EscapeString(display))
	}

	b.WriteString(`    </ol>
  </nav>
</body>
</html>
`)
	return b.String()
}

// content.opf ------------------------------------------------

func renderOPF(book domain.BookMetadata, hasCover bool, chapters []int, lang string) string {
	bookID := bookIdentifier(book.Title)
	modified := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	title := strings.TrimSpace(book.Title)
	if title == "" {
		title = "Untitled"
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="bookid" xml:lang="%[1]s">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="bookid">%[2]s</dc:identifier>
    <dc:title>%[3]s</dc:title>
    <dc:language>%[1]s</dc:language>
    <dc:creator>ainovel-cli</dc:creator>
    <dc:description>%[4]s</dc:description>
    <meta property="dcterms:modified">%[5]s</meta>
  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="css" href="style.css" media-type="text/css"/>
`, lang, html.EscapeString(bookID), html.EscapeString(title), html.EscapeString(book.Synopsis), modified)

	if hasCover {
		b.WriteString(`    <item id="cover" href="cover.xhtml" media-type="application/xhtml+xml"/>` + "\n")
	}
	for _, ch := range chapters {
		fmt.Fprintf(&b, `    <item id="%s" href="%s" media-type="application/xhtml+xml"/>`+"\n",
			chapterID(ch), chapterFileName(ch))
	}

	b.WriteString("  </manifest>\n  <spine>\n")
	if hasCover {
		b.WriteString(`    <itemref idref="cover"/>` + "\n")
	}
	b.WriteString(`    <itemref idref="nav"/>` + "\n")
	for _, ch := range chapters {
		fmt.Fprintf(&b, `    <itemref idref="%s"/>`+"\n", chapterID(ch))
	}
	b.WriteString("  </spine>\n</package>\n")
	return b.String()
}

// bookIdentifier derives a stable UUID string from the novel name.
//
// **Only novelName is used, never the chapter list**: a work's identity should bind to "which
// book this is", not to "the export range" or "how many chapters were written at export time".
// Re-exporting the same book keeps the ID stable, and readers use it to recognise the same work.
// an updated version (whether it is updated is carried by the dcterms:modified timestamp). An
// empty novelName sharing an ID is a known corner case: two unnamed books are the user's own
// responsibility.
func bookIdentifier(novelName string) string {
	h := sha1.New()
	h.Write([]byte(novelName))
	sum := h.Sum(nil)
	// Formatted UUID-style (8-4-4-4-12); strict RFC 4122 is not required — EPUB only needs a unique,
// stable string.
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x",
		sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}
