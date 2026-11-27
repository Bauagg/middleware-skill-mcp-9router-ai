// Package attachment membaca file yang dilampirkan di chat (PDF, Word,
// Excel, CSV, teks, ...) menjadi teks, supaya model yang hanya menerima teks
// tetap bisa membacanya.
package attachment

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"github.com/richardlehane/mscfb"
	"github.com/xuri/excelize/v2"
)

// MaxFileBytes membatasi ukuran satu file yang dibaca.
const MaxFileBytes = 15 << 20

// ErrUnsupported berarti jenis file ini tidak bisa dibaca sebagai teks.
var ErrUnsupported = errors.New("jenis file tidak didukung")

// Extract membaca isi file sebagai teks, berdasarkan ekstensi nama file
// (atau jenis MIME bila ekstensinya tidak dikenal).
func Extract(name, mime string, data []byte) (string, error) {
	if len(data) > MaxFileBytes {
		return "", fmt.Errorf("file lebih dari %d MB", MaxFileBytes>>20)
	}
	switch kind(name, mime) {
	case "pdf":
		return pdfText(data)
	case "docx":
		return docxText(data)
	case "pptx":
		return pptxText(data)
	case "xlsx":
		return xlsxText(data)
	case "doc":
		return docText(data)
	case "text":
		return plainText(data), nil
	}
	return "", ErrUnsupported
}

func kind(name, mime string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf":
		return "pdf"
	case ".docx", ".docm", ".dotx":
		return "docx"
	case ".pptx":
		return "pptx"
	case ".xlsx", ".xlsm":
		return "xlsx"
	case ".doc":
		return "doc"
	case ".txt", ".md", ".markdown", ".csv", ".tsv", ".json", ".xml", ".yaml", ".yml", ".log", ".html", ".htm", ".sql", ".ini":
		return "text"
	}
	switch {
	case mime == "application/pdf":
		return "pdf"
	case strings.Contains(mime, "wordprocessingml"):
		return "docx"
	case strings.Contains(mime, "spreadsheetml"):
		return "xlsx"
	case strings.Contains(mime, "presentationml"):
		return "pptx"
	case mime == "application/msword":
		return "doc"
	case strings.HasPrefix(mime, "text/"), mime == "application/json":
		return "text"
	}
	return ""
}

// plainText membaca teks UTF-8 (atau UTF-16 ber-BOM, mis. CSV ekspor Excel);
// byte yang bukan UTF-8 dibaca sebagai Latin-1.
func plainText(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		b = b[3:]
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return utf16Text(b[2:])
	}
	if utf8.Valid(b) {
		return string(b)
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func utf16Text(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func pdfText(data []byte) (text string, err error) {
	// pustaka PDF bisa panic pada file rusak
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("PDF tidak bisa dibaca: %v", r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("PDF tidak bisa dibaca: %w", err)
	}
	var b strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		fmt.Fprintf(&b, "--- halaman %d ---\n", i)
		b.WriteString(pageText(p.Content().Text))
	}
	if strings.TrimSpace(strings.ReplaceAll(b.String(), "--- halaman", "")) == "" || !hasLetters(b.String()) {
		return "", errors.New("PDF tidak berisi teks (mungkin hasil scan/gambar)")
	}
	return b.String(), nil
}

// pageText menyusun huruf-huruf PDF menjadi baris: baris baru saat posisi
// vertikal berubah, spasi saat jarak antarhuruf lebar.
func pageText(texts []pdf.Text) string {
	var b strings.Builder
	var lastX, lastY, lastW float64
	for i, t := range texts {
		size := t.FontSize
		if size <= 0 {
			size = 10
		}
		if i > 0 {
			switch {
			case abs(t.Y-lastY) > size*0.5:
				b.WriteByte('\n')
			case t.X-(lastX+lastW) > size*0.25 && !strings.HasPrefix(t.S, " "):
				b.WriteByte(' ')
			}
		}
		b.WriteString(t.S)
		lastX, lastY, lastW = t.X, t.Y, t.W
	}
	return collapseBlank(b.String()) + "\n"
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func hasLetters(s string) bool {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
			if n > 20 {
				return true
			}
		}
	}
	return false
}

// openZip membuka file Office (zip).
func openZip(data []byte) (*zip.Reader, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("file Office rusak atau bukan format terbaru (.docx/.xlsx/.pptx)")
	}
	return z, nil
}

func zipFile(z *zip.Reader, name string) ([]byte, error) {
	for _, f := range z.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 64<<20))
		}
	}
	return nil, fmt.Errorf("%s tidak ada di dalam file", name)
}

// xmlText membaca teks dari XML Office: isi elemen bernama textTag (w:t,
// a:t), baris baru setiap akhir elemen paraTag (w:p, a:p), tab untuk w:tab.
func xmlText(raw []byte, textTag, paraTag string) string {
	d := xml.NewDecoder(bytes.NewReader(raw))
	var b strings.Builder
	in := false
	cell := 0 // di dalam sel tabel: paragraf dipisah spasi, sel dipisah " | "
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tc":
				cell++
			case textTag:
				in = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case textTag:
				in = false
			case paraTag:
				if cell > 0 {
					b.WriteByte(' ')
				} else {
					b.WriteByte('\n')
				}
			case "tc":
				cell--
				b.WriteString("| ")
			case "tr":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if in {
				b.Write(t)
			}
		}
	}
	return collapseBlank(b.String())
}

var reBlank = regexp.MustCompile(`\n{3,}`)

func collapseBlank(s string) string { return strings.TrimSpace(reBlank.ReplaceAllString(s, "\n\n")) }

func docxText(data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	raw, err := zipFile(z, "word/document.xml")
	if err != nil {
		return "", err
	}
	return xmlText(raw, "t", "p"), nil
}

var reSlide = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

func pptxText(data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	type slide struct {
		n    int
		name string
	}
	var slides []slide
	for _, f := range z.File {
		if m := reSlide.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			slides = append(slides, slide{n, f.Name})
		}
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].n < slides[j].n })
	var b strings.Builder
	for _, s := range slides {
		raw, err := zipFile(z, s.name)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "--- slide %d ---\n%s\n", s.n, xmlText(raw, "t", "p"))
	}
	return b.String(), nil
}

// xlsxText menulis setiap sheet sebagai CSV (nilai yang tampil di Excel).
func xlsxText(data []byte) (string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("file Excel rusak atau bukan .xlsx")
	}
	defer f.Close()
	var b strings.Builder
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil || len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&b, "--- sheet %s ---\n", sheet)
		w := csv.NewWriter(&b)
		for _, row := range rows {
			_ = w.Write(row)
		}
		w.Flush()
	}
	return b.String(), nil
}

// docText membaca Word lama (.doc, Word 97-2003). Teks dokumen ada di
// stream WordDocument, tersusun dari potongan (piece table di stream
// 0Table/1Table) yang masing-masing 8-bit (cp1252) atau UTF-16.
func docText(data []byte) (string, error) {
	r, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("bukan file Word .doc")
	}
	streams := map[string][]byte{}
	for e, err := r.Next(); err == nil; e, err = r.Next() {
		switch e.Name {
		case "WordDocument", "0Table", "1Table":
			streams[e.Name], _ = io.ReadAll(io.LimitReader(e, 64<<20))
		}
	}
	doc := streams["WordDocument"]
	if len(doc) < 0x1AA {
		return "", errors.New("file .doc tidak dikenali: simpan ulang sebagai .docx")
	}
	le32 := func(b []byte, off int) int { return int(binary.LittleEndian.Uint32(b[off:])) }
	table := streams["0Table"]
	if binary.LittleEndian.Uint16(doc[0x0A:])&0x0200 != 0 {
		table = streams["1Table"]
	}
	ccpText := le32(doc, 0x4C)
	fcClx, lcbClx := le32(doc, 0x1A2), le32(doc, 0x1A6)
	if fcClx+lcbClx > len(table) || lcbClx == 0 {
		return "", errors.New("file .doc rusak: tabel teks tidak ditemukan")
	}
	clx := table[fcClx : fcClx+lcbClx]
	// lewati Prc (0x01 + panjang 2 byte), cari Pcdt (0x02 + panjang 4 byte)
	i := 0
	for i < len(clx) && clx[i] == 0x01 {
		if i+3 > len(clx) {
			break
		}
		i += 3 + int(binary.LittleEndian.Uint16(clx[i+1:]))
	}
	if i+5 > len(clx) || clx[i] != 0x02 {
		return "", errors.New("file .doc rusak: piece table tidak ditemukan")
	}
	plc := clx[i+5:]
	if n := le32(clx, i+1); n <= len(plc) {
		plc = plc[:n]
	}
	pieces := (len(plc) - 4) / 12
	var b strings.Builder
	for k := 0; k < pieces; k++ {
		cpStart, cpEnd := le32(plc, k*4), le32(plc, (k+1)*4)
		if cpStart >= ccpText {
			break
		}
		cpEnd = min(cpEnd, ccpText)
		fc := le32(plc, (pieces+1)*4+k*8+2)
		n := cpEnd - cpStart
		if fc&0x40000000 != 0 { // 8-bit
			off := (fc &^ 0x40000000) / 2
			if off+n > len(doc) {
				break
			}
			for _, c := range doc[off : off+n] {
				b.WriteRune(cp1252(c))
			}
		} else { // UTF-16
			if fc+2*n > len(doc) {
				break
			}
			b.WriteString(utf16Text(doc[fc : fc+2*n]))
		}
	}
	return wordClean(b.String()), nil
}

// cp1252 menerjemahkan byte Windows-1252 (cukup tanda baca yang umum).
func cp1252(c byte) rune {
	switch c {
	case 0x91, 0x92:
		return '\''
	case 0x93, 0x94:
		return '"'
	case 0x96, 0x97:
		return '-'
	case 0x85:
		return '…'
	case 0x80:
		return '€'
	}
	return rune(c)
}

// wordClean mengubah karakter kendali Word menjadi teks biasa: paragraf (\r)
// dan ganti baris (\v) jadi baris baru, akhir sel (\a) jadi " | " (dua
// berturut-turut = akhir baris tabel, jadi baris baru), dan kode field
// (\x13 kode \x14 hasil \x15) hanya diambil hasilnya.
func wordClean(s string) string {
	var b strings.Builder
	depth, inCode, prevCell := 0, false, false
	for _, r := range s {
		switch r {
		case 0x13:
			depth++
			inCode = true
			continue
		case 0x14:
			inCode = false
			continue
		case 0x15:
			if depth > 0 {
				depth--
			}
			inCode = false
			continue
		}
		if inCode {
			continue
		}
		cell := r == 0x07
		switch {
		case cell && prevCell:
			b.WriteByte('\n')
		case cell:
			b.WriteString(" | ")
		case r == '\r', r == 0x0B, r == 0x0C:
			b.WriteByte('\n')
		case r == '\t', r >= 0x20:
			b.WriteRune(r)
		}
		prevCell = cell
	}
	return collapseBlank(b.String())
}
