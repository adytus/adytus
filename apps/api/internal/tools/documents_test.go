package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func zipOf(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const wordNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

func TestReadDocx(t *testing.T) {
	doc := zipOf(t, map[string]string{"word/document.xml": `<?xml version="1.0"?>
<w:document ` + wordNS + `><w:body>
<w:p><w:pPr><w:pStyle w:val="Title"/></w:pPr><w:r><w:t>Quarterly</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>Sales</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">Up </w:t></w:r><w:r><w:t>12%</w:t></w:r><w:del><w:r><w:delText>gone</w:delText></w:r></w:del></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/></w:numPr></w:pPr><w:r><w:t>first point</w:t></w:r></w:p>
<w:tbl>
 <w:tr><w:tc><w:p><w:r><w:t>Region</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Total</w:t></w:r></w:p></w:tc></w:tr>
 <w:tr><w:tc><w:p><w:r><w:t>North|East</w:t></w:r></w:p></w:tc><w:tc>
   <w:tbl><w:tr><w:tc><w:p><w:r><w:t>inner</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
   <w:p><w:r><w:t>40</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
<w:p><w:r><w:t>After</w:t></w:r></w:p>
</w:body></w:document>`})
	got, err := NewDocuments().Read(context.Background(), "q.docx", doc, 0)
	if err != nil {
		t.Fatal(err)
	}
	body := "# Quarterly\n\n## Sales\n\nUp 12%\n\n- first point\n\n" +
		"| Region | Total |\n| --- | --- |\n| North\\|East | inner 40 |\n\nAfter"
	want := fmt.Sprintf("[q.docx: Word document, %d characters]\n%s", len(body), body)
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestReadXlsx(t *testing.T) {
	f := excelize.NewFile()
	f.SetSheetRow("Sheet1", "A1", &[]any{"Item", "Qty", "Price"})
	f.SetSheetRow("Sheet1", "A2", &[]any{"Pens", 3, 1.5})
	f.SetCellFormula("Sheet1", "C3", "SUM(C2:C2)")
	f.NewSheet("Empty")
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := NewDocuments().Read(context.Background(), "s.xlsx", buf.Bytes(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Excel workbook", "## Sheet: Sheet1", "| Item | Qty | Price |", "| Pens | 3 | 1.5 |", "## Sheet: Empty\n\n(empty)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestReadPptx(t *testing.T) {
	slide := func(text string) string {
		return `<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>` + text + `</a:t></a:r></a:p><a:p><a:r><a:t>more</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
	}
	doc := zipOf(t, map[string]string{
		"ppt/presentation.xml":            "<p:presentation/>",
		"ppt/slides/slide10.xml":          slide("ten"),
		"ppt/slides/slide2.xml":           slide("two"),
		"ppt/notesSlides/notesSlide2.xml": `<p:notes xmlns:p="p" xmlns:a="a"><a:p><a:r><a:t>say hi</a:t></a:r></a:p></p:notes>`,
	})
	got, err := NewDocuments().Read(context.Background(), "d.pptx", doc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "## Slide 1\n\ntwo\nmore\n\nNotes: say hi\n\n## Slide 2\n\nten\nmore") {
		t.Fatalf("got\n%s", got)
	}
}

// testPDF builds a PDF with one line of text per page.
func testPDF(pages ...string) []byte {
	var objs []string
	kids := ""
	for i, text := range pages {
		page, content := 4+2*i, 5+2*i
		kids += fmt.Sprintf("%d 0 R ", page)
		stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", content),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	objs = append([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, len(pages)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, objs...)
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}

func TestReadPDF(t *testing.T) {
	docs := NewDocuments()
	t.Cleanup(func() { docs.Close() })
	got, err := docs.Read(context.Background(), "a.pdf", testPDF("Hello from page one", "Second page"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "## Page 1\n\nHello from page one\n\n## Page 2\n\nSecond page") {
		t.Fatalf("got\n%s", got)
	}
	if _, err := docs.Read(context.Background(), "b.pdf", []byte("%PDF-1.4 garbage"), 0); err == nil {
		t.Fatal("a broken PDF should fail")
	}
}

func TestReadChunksAndOthers(t *testing.T) {
	docs := NewDocuments()
	long := strings.Repeat("é", docChunk+10)
	got, err := docs.Read(context.Background(), "l.txt", []byte(long), 0)
	if err != nil || !strings.HasSuffix(got, fmt.Sprintf("[more follows: call again with start=%d]", docChunk)) {
		t.Fatalf("first chunk: %v %q", err, got[len(got)-60:])
	}
	got, err = docs.Read(context.Background(), "l.txt", []byte(long), docChunk)
	if err != nil || !strings.HasSuffix(got, "]\néééééééééé") {
		t.Fatalf("second chunk: %v %q", err, got)
	}
	if _, err := docs.Read(context.Background(), "l.txt", []byte(long), 1<<30); err == nil {
		t.Fatal("start past the end should fail")
	}

	got, err = docs.Read(context.Background(), "p.png", testPNG(t, 30, 10), 0)
	if err != nil || !strings.Contains(got, "PNG image, 30x10") {
		t.Fatalf("image: %v %q", err, got)
	}
	for name, data := range map[string][]byte{
		"x.doc": {0xd0, 0xcf, 0x11, 0xe0, 0, 1},
		"x.zip": zipOf(t, map[string]string{"a.txt": "a"}),
		"x.bin": {0, 1, 2, 0xff},
	} {
		if _, err := docs.Read(context.Background(), name, data, 0); err == nil {
			t.Errorf("%s should fail", name)
		}
	}
}
