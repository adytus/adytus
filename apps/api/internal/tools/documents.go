package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/xuri/excelize/v2"
	_ "golang.org/x/image/webp"
)

// Documents turns the chat's files into text for the model: Word, Excel,
// PowerPoint, PDF, plain text, and a description of images. It reads, and
// never changes anything; the python tool is for that. PDFs go through
// PDFium compiled to WebAssembly, with no filesystem and bounded memory, so
// a hostile file can only hurt the parse.
type Documents struct {
	once    sync.Once
	pool    pdfium.Pool
	poolErr error
}

func NewDocuments() *Documents { return &Documents{} }

func (d *Documents) Close() error {
	if d.pool != nil {
		return d.pool.Close()
	}
	return nil
}

const (
	// maxDocChars bounds the text taken from one file; past it the rest is
	// left to the python tool.
	maxDocChars = 1 << 20
	// docChunk is how much text one call returns.
	docChunk = 12000
	// maxPartBytes bounds one decompressed part of an Office file.
	maxPartBytes = 64 << 20
	// maxSheetRows bounds the rows read from one sheet.
	maxSheetRows = 2000
	pdfTimeout   = 30 * time.Second
)

// Read returns the text of a file, from rune offset start, at most docChunk
// runes of it, with a line saying what the file is and where the text sits.
func (d *Documents) Read(ctx context.Context, name string, data []byte, start int) (string, error) {
	kind, text, err := d.extract(ctx, name, data)
	if err != nil {
		return "", err
	}
	runes := []rune(text)
	total := len(runes)
	if start < 0 || (start > 0 && start >= total) {
		return "", fmt.Errorf("start is past the end of the text (%d characters)", total)
	}
	end := min(start+docChunk, total)
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s: %s, %d characters", name, kind, total)
	if start > 0 || end < total {
		fmt.Fprintf(&sb, "; showing %d to %d", start, end)
	}
	sb.WriteString("]\n")
	sb.WriteString(string(runes[start:end]))
	if end < total {
		fmt.Fprintf(&sb, "\n[more follows: call again with start=%d]", end)
	}
	return sb.String(), nil
}

// extract names the file's format and returns its text.
func (d *Documents) extract(ctx context.Context, name string, data []byte) (kind, text string, err error) {
	ext := strings.ToLower(path.Ext(name))
	switch {
	case bytes.HasPrefix(data, []byte("%PDF-")):
		text, err = d.pdf(ctx, data)
		return "PDF", text, err
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return office(data, ext)
	}
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		return "image", fmt.Sprintf("A %s image, %dx%d pixels. There is no text to read; you see images attached to the chat when the model has vision, and the python tool can process it with Pillow.", strings.ToUpper(format), cfg.Width, cfg.Height), nil
	}
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		return "text", string(data), nil
	}
	switch ext {
	case ".doc", ".xls", ".ppt":
		return "", "", fmt.Errorf("%s is an old binary Office format that can't be read; ask for a .docx, .xlsx or .pptx", ext)
	}
	return "", "", errors.New("not a format this tool reads (PDF, Word, Excel, PowerPoint, text or an image); try the python tool")
}

func office(data []byte, ext string) (kind, text string, err error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", "", fmt.Errorf("not a valid %s file: %w", ext, err)
	}
	has := func(name string) bool {
		return slices.ContainsFunc(zr.File, func(f *zip.File) bool { return f.Name == name })
	}
	switch {
	case has("word/document.xml"):
		text, err = docxText(zr)
		return "Word document", text, err
	case has("xl/workbook.xml"):
		text, err = xlsxText(data)
		return "Excel workbook", text, err
	case has("ppt/presentation.xml"):
		text, err = pptxText(zr)
		return "PowerPoint presentation", text, err
	}
	return "", "", errors.New("a zip archive, not a Word, Excel or PowerPoint file; the python tool can open it with zipfile")
}

// textBuilder collects text up to maxDocChars and notes where it stopped.
type textBuilder struct {
	strings.Builder
	full bool
}

func (b *textBuilder) add(s string) {
	if b.full {
		return
	}
	if b.Len()+len(s) > maxDocChars {
		b.WriteString(s[:maxDocChars-b.Len()])
		b.WriteString("\n\n[the rest is cut: the document is too long to read here; use the python tool]")
		b.full = true
		return
	}
	b.WriteString(s)
}

func openPart(zr *zip.Reader, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			return struct {
				io.Reader
				io.Closer
			}{io.LimitReader(rc, maxPartBytes), rc}, nil
		}
	}
	return nil, fmt.Errorf("%s is missing", name)
}

// docxText renders word/document.xml as Markdown: headings, list items,
// paragraphs and tables. Tables nested in a cell are flattened into it.
func docxText(zr *zip.Reader) (string, error) {
	rc, err := openPart(zr, "word/document.xml")
	if err != nil {
		return "", err
	}
	defer rc.Close()

	var out textBuilder
	var para strings.Builder
	var style string
	var list bool
	// One table per nesting level: rows of cells.
	var tables [][][]string
	inText := false

	endPara := func() {
		t := strings.TrimSpace(para.String())
		para.Reset()
		defer func() { style, list = "", false }()
		if len(tables) > 0 {
			rows := tables[len(tables)-1]
			if len(rows) > 0 && len(rows[len(rows)-1]) > 0 && t != "" {
				cell := &rows[len(rows)-1][len(rows[len(rows)-1])-1]
				*cell = strings.TrimSpace(*cell + " " + t)
			}
			return
		}
		if t == "" {
			return
		}
		switch {
		case style == "Title":
			t = "# " + t
		case strings.HasPrefix(strings.ToLower(style), "heading"):
			n, err := strconv.Atoi(style[len("heading"):])
			if err != nil || n < 1 {
				n = 1
			}
			t = strings.Repeat("#", min(n, 6)) + " " + t
		case list:
			t = "- " + t
		}
		out.add(t + "\n\n")
	}

	dec := xml.NewDecoder(rc)
	for !out.full {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("the document is damaged: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				para.WriteString("\t")
			case "br", "cr":
				para.WriteString("\n")
			case "pStyle":
				style = attr(t, "val")
			case "numPr":
				list = true
			case "tbl":
				// A nested one is folded into the outer cell at its end.
				tables = append(tables, nil)
			case "tr":
				if len(tables) > 0 {
					tables[len(tables)-1] = append(tables[len(tables)-1], []string{})
				}
			case "tc":
				if n := len(tables); n > 0 {
					rows := tables[n-1]
					if len(rows) > 0 {
						rows[len(rows)-1] = append(rows[len(rows)-1], "")
					}
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				endPara()
			case "tbl":
				n := len(tables)
				if n == 0 {
					break
				}
				rows := tables[n-1]
				tables = tables[:n-1]
				if len(tables) > 0 {
					// Fold the nested table into the outer cell.
					outer := tables[len(tables)-1]
					if len(outer) > 0 && len(outer[len(outer)-1]) > 0 {
						cell := &outer[len(outer)-1][len(outer[len(outer)-1])-1]
						for _, r := range rows {
							*cell = strings.TrimSpace(*cell + " " + strings.Join(r, " "))
						}
					}
					break
				}
				out.add(markdownTable(rows) + "\n")
			}
		case xml.CharData:
			if inText {
				para.Write(t)
			}
		}
	}
	return strings.TrimSpace(out.String()), nil
}

func attr(e xml.StartElement, local string) string {
	for _, a := range e.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

var slideNum = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

// pptxText lists each slide's text, a line per paragraph, with its notes.
func pptxText(zr *zip.Reader) (string, error) {
	type slide struct {
		n    int
		name string
	}
	var slides []slide
	for _, f := range zr.File {
		if m := slideNum.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			slides = append(slides, slide{n, f.Name})
		}
	}
	slices.SortFunc(slides, func(a, b slide) int { return a.n - b.n })

	var out textBuilder
	for i, s := range slides {
		body, err := drawingText(zr, s.name)
		if err != nil {
			return "", err
		}
		out.add(fmt.Sprintf("## Slide %d\n\n%s\n\n", i+1, body))
		notes := fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", s.n)
		if n, err := drawingText(zr, notes); err == nil && n != "" {
			out.add("Notes: " + n + "\n\n")
		}
		if out.full {
			break
		}
	}
	return strings.TrimSpace(out.String()), nil
}

// drawingText joins the a:t runs of a DrawingML part, a line per paragraph.
func drawingText(zr *zip.Reader, name string) (string, error) {
	rc, err := openPart(zr, name)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	var lines []string
	var line strings.Builder
	inText := false
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%s is damaged: %w", name, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "br":
				line.WriteString("\n")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if s := strings.TrimSpace(line.String()); s != "" {
					lines = append(lines, s)
				}
				line.Reset()
			}
		case xml.CharData:
			if inText {
				line.Write(t)
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}

// xlsxText renders each sheet as a Markdown table of its displayed values.
func xlsxText(data []byte) (string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{
		UnzipSizeLimit:    256 << 20,
		UnzipXMLSizeLimit: maxPartBytes,
	})
	if err != nil {
		return "", fmt.Errorf("the workbook can't be read: %w", err)
	}
	defer f.Close()

	var out textBuilder
	for _, sheet := range f.GetSheetList() {
		out.add("## Sheet: " + sheet + "\n\n")
		rows, err := f.Rows(sheet)
		if err != nil {
			out.add("(can't be read: " + err.Error() + ")\n\n")
			continue
		}
		var table [][]string
		more := false
		for rows.Next() {
			if len(table) == maxSheetRows {
				more = true
				break
			}
			cols, err := rows.Columns()
			if err != nil {
				rows.Close()
				return "", err
			}
			table = append(table, cols)
		}
		rows.Close()
		// Leading empty rows carry nothing.
		for len(table) > 0 && slices.IndexFunc(table[0], func(s string) bool { return s != "" }) < 0 {
			table = table[1:]
		}
		if len(table) == 0 {
			out.add("(empty)\n\n")
			continue
		}
		out.add(markdownTable(table))
		if more {
			out.add(fmt.Sprintf("\n[only the first %d rows are shown; use the python tool (openpyxl or pandas) for all of them]\n", maxSheetRows))
		}
		out.add("\n")
		if out.full {
			break
		}
	}
	return strings.TrimSpace(out.String()), nil
}

// markdownTable renders rows with the first as the header, padded to the
// widest row and without trailing empty columns.
func markdownTable(rows [][]string) string {
	width := 0
	for _, r := range rows {
		for i := len(r) - 1; i >= 0; i-- {
			if strings.TrimSpace(r[i]) != "" {
				width = max(width, i+1)
				break
			}
		}
	}
	if width == 0 {
		return ""
	}
	cell := strings.NewReplacer("|", `\|`, "\r\n", " ", "\n", " ", "\t", " ")
	var sb strings.Builder
	line := func(r []string) {
		sb.WriteString("|")
		for i := range width {
			v := ""
			if i < len(r) {
				v = strings.TrimSpace(cell.Replace(r[i]))
			}
			sb.WriteString(" " + v + " |")
		}
		sb.WriteString("\n")
	}
	line(rows[0])
	sb.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
	for _, r := range rows[1:] {
		line(r)
	}
	return sb.String()
}

// pdf returns each page's text under a heading.
func (d *Documents) pdf(ctx context.Context, data []byte) (string, error) {
	d.once.Do(func() {
		d.pool, d.poolErr = webassembly.Init(webassembly.Config{
			MinIdle: 0, MaxIdle: 1, MaxTotal: 2,
			// No mounts: PDFium sees no files but the one it is given.
			FSConfig: wazero.NewFSConfig(),
			// 512 MB of WebAssembly pages; PDFium needs exception handling.
			RuntimeConfig: wazero.NewRuntimeConfig().WithMemoryLimitPages(8192).
				WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling),
		})
	})
	if d.poolErr != nil {
		return "", fmt.Errorf("the PDF reader didn't start: %w", d.poolErr)
	}
	ctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	inst, err := d.pool.GetInstanceWithContext(ctx)
	if err != nil {
		return "", err
	}
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := pdfText(inst, data)
		done <- result{text, err}
	}()
	select {
	case r := <-done:
		inst.Close()
		return r.text, r.err
	case <-ctx.Done():
		// A page that never finishes takes the instance with it.
		_ = inst.Kill()
		return "", errors.New("the PDF took too long to read; try the python tool (pypdf or pdfminer)")
	}
}

func pdfText(inst pdfium.Pdfium, data []byte) (string, error) {
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		if strings.Contains(err.Error(), "password") {
			return "", errors.New("the PDF is password-protected")
		}
		return "", fmt.Errorf("the PDF can't be read: %w", err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return "", err
	}
	var out textBuilder
	empty := 0
	for i := range count.PageCount {
		page, err := inst.GetPageText(&requests.GetPageText{Page: requests.Page{
			ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i},
		}})
		if err != nil {
			return "", fmt.Errorf("page %d: %w", i+1, err)
		}
		text := strings.TrimSpace(strings.ReplaceAll(page.Text, "\r\n", "\n"))
		if text == "" {
			empty++
			text = "(no text on this page)"
		}
		out.add(fmt.Sprintf("## Page %d\n\n%s\n\n", i+1, text))
		if out.full {
			break
		}
	}
	if empty == count.PageCount && count.PageCount > 0 {
		out.add("[no page has text: the PDF is probably scanned images, and there is no OCR here]")
	}
	return strings.TrimSpace(out.String()), nil
}
