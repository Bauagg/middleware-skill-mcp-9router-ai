package attachment

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Marker mengawali teks lampiran yang dimasukkan Expand, supaya teks yang
// diketik user bisa dibedakan dari isi file (mis. untuk memilih skill).
const Marker = "<lampiran "

// MaxTextRunes membatasi teks satu lampiran yang dikirim ke model.
const MaxTextRunes = 60_000

// Expand mengganti bagian pesan berjenis "file" (format OpenAI:
// {"type":"file","file":{"filename":"a.pdf","file_data":"data:...;base64,..."}})
// dengan teks isinya, karena kebanyakan model di 9Router hanya menerima teks
// dan gambar. Gambar (image_url) dibiarkan. File yang gagal dibaca diganti
// keterangan singkat, bukan menggagalkan request. Mengembalikan body baru dan
// jumlah file yang diganti (0 = body tidak berubah).
func Expand(raw []byte) ([]byte, int, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return raw, 0, err
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(body["messages"], &messages); err != nil {
		return raw, 0, nil // bukan urusan lampiran: biarkan validasi lain yang menilai
	}
	n := 0
	for _, m := range messages {
		var parts []map[string]json.RawMessage
		if json.Unmarshal(m["content"], &parts) != nil {
			continue // content berupa string
		}
		changed := false
		for i, p := range parts {
			var typ string
			_ = json.Unmarshal(p["type"], &typ)
			if typ != "file" {
				continue
			}
			parts[i] = map[string]json.RawMessage{"type": json.RawMessage(`"text"`), "text": mustJSON(fileText(p["file"]))}
			changed = true
			n++
		}
		if changed {
			m["content"] = mustJSON(parts)
		}
	}
	if n == 0 {
		return raw, 0, nil
	}
	body["messages"] = mustJSON(messages)
	out, err := json.Marshal(body)
	if err != nil {
		return raw, 0, err
	}
	return out, n, nil
}

// fileText membaca satu bagian file menjadi teks berlabel.
func fileText(raw json.RawMessage) string {
	var f struct {
		Filename string `json:"filename"`
		FileData string `json:"file_data"`
		FileID   string `json:"file_id"`
	}
	_ = json.Unmarshal(raw, &f)
	name := strings.TrimSpace(f.Filename)
	if name == "" {
		name = "file"
	}
	open := fmt.Sprintf("%snama=%q>\n", Marker, name)
	const end = "\n</lampiran>"
	if f.FileData == "" {
		return open + "(isi file tidak dikirim: middleware hanya membaca file_data berisi data URL base64)" + end
	}
	mime, data, err := decodeDataURL(f.FileData)
	if err != nil {
		return open + "(file tidak bisa dibaca: " + err.Error() + ")" + end
	}
	text, err := Extract(name, mime, data)
	if err != nil {
		return open + "(file tidak bisa dibaca: " + err.Error() + ")" + end
	}
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > MaxTextRunes {
		text = string(r[:MaxTextRunes]) + fmt.Sprintf("\n... (dipotong: hanya %d karakter pertama dari %d yang dikirim)", MaxTextRunes, len(r))
	}
	if text == "" {
		text = "(file kosong)"
	}
	return open + text + end
}

// decodeDataURL membaca "data:<mime>;base64,<isi>" (atau base64 polos).
func decodeDataURL(s string) (mime string, data []byte, err error) {
	payload := s
	if rest, ok := strings.CutPrefix(s, "data:"); ok {
		meta, b64, found := strings.Cut(rest, ",")
		if !found || !strings.HasSuffix(meta, ";base64") {
			return "", nil, fmt.Errorf("data URL tidak berformat base64")
		}
		mime, payload = strings.TrimSuffix(meta, ";base64"), b64
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > MaxFileBytes+3 {
		return "", nil, fmt.Errorf("file lebih dari %d MB", MaxFileBytes>>20)
	}
	data, err = base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", nil, fmt.Errorf("base64 rusak")
	}
	return mime, data, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
