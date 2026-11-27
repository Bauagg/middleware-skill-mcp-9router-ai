package aiaccount

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/llmtool"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// VideoTool adalah nama tool pembuat video di chat.
const VideoTool = "generate_video"

const maxVideoPromptRunes = 2000

// videoIntent menandai pesan yang meminta video.
var videoIntent = []string{"video", "animasi", "animate", "animation", "klip", "clip", "veo", "film"}

func videoToolSpec() json.RawMessage {
	return llmtool.Function(VideoTool,
		"Buat video pendek (sekitar 8 detik, mis. dengan Veo lewat akun Gemini user) dari deskripsi teks. "+
			"Pakai hanya bila user meminta dibuatkan video/animasi. Prosesnya 1-5 menit dan limit hariannya kecil, "+
			"jadi jangan memanggil ulang bila gagal kecuali user memintanya. Tulis prompt yang detail dalam bahasa Inggris: "+
			"subjek, gerakan, suasana, gaya visual, dan sudut kamera.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{"type": "string", "description": "deskripsi video yang detail, dalam bahasa Inggris"},
			},
			"required": []string{"prompt"},
		})
}

// Select menawarkan tool generate_video bila pesan menyinggung video dan user
// punya akun AI pembuat video yang limitnya belum habis.
func (s *Service) Select(ctx context.Context, query string) []llmtool.Spec {
	if userctx.From(ctx) == "" || !hasVideoIntent(query) || !s.HasUsable(ctx, kindVideo) {
		return nil
	}
	return []llmtool.Spec{{Name: VideoTool, Source: "akun-ai", Definition: s.videoSpec}}
}

func hasVideoIntent(text string) bool {
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		for _, p := range videoIntent {
			if strings.HasPrefix(w, p) {
				return true
			}
		}
	}
	return false
}

// Has melaporkan apakah tool ini milik pembuat video.
func (s *Service) Has(_ context.Context, name string) bool { return s.Enabled() && name == VideoTool }

// Call membuat video lalu mengembalikan link-nya ke model.
func (s *Service) Call(ctx context.Context, _ string, args string) llmtool.Result {
	var a struct {
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(args)), &a); err != nil {
		return llmtool.Result{Text: "argumen tidak valid: kirim {\"prompt\": \"...\"}", IsError: true}
	}
	a.Prompt = strings.TrimSpace(a.Prompt)
	if a.Prompt == "" {
		return llmtool.Result{Text: "prompt kosong: jelaskan video yang ingin dibuat", IsError: true}
	}
	if r := []rune(a.Prompt); len(r) > maxVideoPromptRunes {
		a.Prompt = string(r[:maxVideoPromptRunes])
	}

	v, err := s.GenerateVideo(ctx, userctx.From(ctx), a.Prompt)
	if err != nil {
		text := "gagal membuat video: " + err.Error()
		if !errors.Is(err, ErrVideoBusy) {
			text += ". Sampaikan alasannya ke user apa adanya. Jangan mencoba lagi kecuali user memintanya: setiap percobaan memakai limit video harian."
		}
		return llmtool.Result{Text: text, IsError: true}
	}
	return llmtool.Result{Text: "Video berhasil dibuat.\nURL: " + v.URL +
		"\nTampilkan ke user sebagai link markdown persis: [▶ Putar video](" + v.URL + ")" +
		"\nKamu tidak bisa menonton video ini. Jangan mengklaim detail isinya; cukup sebut video dibuat " +
		"sesuai permintaan, lalu tawarkan revisi bila hasilnya belum pas."}
}
