package aiaccount

import (
	"strings"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
)

// maxPromptRunes membatasi panjang percakapan yang dikirim ke provider web
// (bukan API) dalam satu pesan; pesan lama dibuang lebih dulu.
const maxPromptRunes = 60_000

// transcript menulis percakapan berformat OpenAI sebagai satu teks untuk
// provider web yang hanya menerima satu pesan: instruksi system di depan,
// riwayat (yang terbaru dipertahankan bila terlalu panjang), lalu pesan
// terakhir user.
func transcript(messages []clients.ChatMessage, limit int) string {
	var system []string
	var turns []clients.ChatMessage
	for _, m := range messages {
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		switch m.Role {
		case "system", "developer":
			system = append(system, text)
		default:
			turns = append(turns, clients.ChatMessage{Role: m.Role, Content: text})
		}
	}
	if len(turns) == 0 {
		return strings.Join(system, "\n\n")
	}
	last := turns[len(turns)-1]
	turns = turns[:len(turns)-1]
	if len(turns) == 0 && len(system) == 0 {
		return last.Content
	}

	var head strings.Builder
	if len(system) > 0 {
		head.WriteString("[Instruksi untukmu — ikuti, jangan dikutip]\n")
		head.WriteString(strings.Join(system, "\n\n"))
		head.WriteString("\n\n")
	}
	tail := "[Pesan terbaru dari user — jawab ini]\n" + last.Content

	// riwayat diisi dari yang terbaru sampai sisa batas habis
	budget := limit - runes(head.String()) - runes(tail)
	var hist []string
	for i := len(turns) - 1; i >= 0 && budget > 0; i-- {
		line := label(turns[i].Role) + ": " + turns[i].Content
		if n := runes(line); n > budget {
			if budget < 200 {
				break // potongan sependek itu tidak berguna
			}
			line = "..." + string([]rune(line)[n-budget+3:])
		}
		budget -= runes(line) + 2
		hist = append([]string{line}, hist...)
	}
	var b strings.Builder
	b.WriteString(head.String())
	if len(hist) > 0 {
		b.WriteString("[Percakapan sebelumnya]\n")
		b.WriteString(strings.Join(hist, "\n\n"))
		b.WriteString("\n\n")
	}
	b.WriteString(tail)
	return b.String()
}

func label(role string) string {
	switch role {
	case "assistant":
		return "Asisten"
	case "tool":
		return "Hasil tool"
	}
	return "User"
}

func runes(s string) int { return len([]rune(s)) }
