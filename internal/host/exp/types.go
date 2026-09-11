// Package exp implements export for completed chapters.
//
// It mirrors imp/: pure local IO, no LLM dependency, no store mutation. Export can run
// concurrently with the Engine (it only reads Progress and final chapter texts) and is a
// cross-cutting capability.
//
// TXT and EPUB are currently supported.
package exp

import "github.com/voocel/ainovel-cli/internal/store"

// Format identifies the export format.
type Format string

const (
	// FormatTXT is plain-text output.
	FormatTXT Format = "txt"
	// FormatEPUB is a standard EPUB 3 container (zip + xhtml).
	FormatEPUB Format = "epub"
)

// Options controls export behaviour. The zero value means "export the whole book to the
// default path, erroring when the file exists".
//
// Layout: book title -> volume divider -> chapter prose. Two kinds of internal data never
// enter the export: premise (the creative blueprint, holding back-office metadata such as target
// reader / core selling point / writing-prohibited zones, meant for the author and engine rather than as a
// reader-facing preface) and arc dividers (from a reader's view arcs are an over-fine internal
// structure). The book title and volume dividers are always kept.
type Options struct {
	// An empty Format infers from the OutPath extension (.txt -> TXT, .epub -> EPUB); when
	// OutPath is empty too it falls back to FormatTXT. SDK callers may set it explicitly to skip
	// the inference.
	Format Format

	// OutPath is the output file path; empty means {novelDir}/{BookMetadata.Title}.{ext}.
	OutPath string

	// From / To is the chapter range, inclusive. 0 means from chapter 1 / to the last chapter.
	// Incomplete chapters inside the range are skipped and recorded in Result.Skipped, which is
	// not an error.
	From, To int

	// Overwrite decides whether to overwrite an existing file; the default refuses.
	Overwrite bool
}

// Deps holds the dependencies Run needs. store only; export needs no LLM, prompt or bundle.
type Deps struct {
	Store *store.Store
}

// Result summarises one successful export.
type Result struct {
	// Path is the file path actually written (absolute, or relative as the caller passed it).
	Path string
	// Chapters is the number of chapters actually written.
	Chapters int
	// Bytes is the file size in bytes (UTF-8).
	Bytes int
	// Skipped lists chapter numbers inside the requested range that were not complete.
	Skipped []int
}
