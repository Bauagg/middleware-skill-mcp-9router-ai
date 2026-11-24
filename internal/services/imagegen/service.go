// Package imagegen membuat gambar lewat chat. 9Router tidak membuat gambar di
// dalam chat, jadi middleware memberi model tool bawaan "generate_image":
//
//  1. tool hanya ditawarkan bila pesan user menyinggung gambar (gambar,
//     ilustrasi, logo, foto, ...), supaya chat biasa tidak bertambah token;
//  2. saat model memanggilnya, middleware meminta gambar ke 9Router milik user
//     (POST /images/generations, key user). Model gambar tidak ditulis mati:
//     daftarnya diambil dari akun 9Router user (GET /models/image) lalu
//     dicoba berurutan;
//  3. gambar disimpan di IMAGE_PATH dan disajikan di BASE_URL/images/<nama>;
//     URL dan prompt-nya dicatat di tabel t_generated_images;
//  4. model menerima link-nya dan menampilkannya di jawaban sebagai markdown
//     ![...](link), sehingga gambar tampil di aplikasi chat.
package imagegen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/llmtool"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// ToolName adalah nama tool yang dilihat model.
const ToolName = "generate_image"

const maxPromptRunes = 4000

// intentWords dan intentPrefixes menandai pesan yang meminta gambar.
var intentWords = map[string]bool{
	"image": true, "images": true, "picture": true, "foto": true, "photo": true,
	"logo": true, "ikon": true, "icon": true, "wallpaper": true, "avatar": true,
	"banner": true, "poster": true, "thumbnail": true, "sketsa": true, "sketch": true,
	"draw": true, "drawing": true, "render": true, "artwork": true, "karikatur": true,
	"maskot": true, "mascot": true, "stiker": true, "sticker": true, "kartun": true,
	"visual": true, "lukisan": true,
}
var intentPrefixes = []string{"gambar", "ilustrasi", "lukis", "illustrat"}

// Generator membuat gambar dan mendaftar model gambar akun user;
// clients.ImageClient memenuhinya.
type Generator interface {
	Generate(ctx context.Context, model, prompt, size string) (*clients.GeneratedImage, error)
	ImageModels(ctx context.Context) ([]string, error)
}

// Daftar model gambar per akun di-cache supaya tidak diambil di setiap
// request; kegagalan di-cache lebih singkat agar cepat pulih.
const (
	modelsTTL     = 10 * time.Minute
	modelsErrTTL  = time.Minute
	modelsTimeout = 5 * time.Second
)

type modelList struct {
	models  []string
	expires time.Time
}

// Lama sebuah model gambar dilewati setelah ditolak: akun tidak berhak
// (mis. ChatGPT Free) tidak akan berubah dalam hitungan menit, sedangkan
// "kapasitas penuh" biasanya cepat pulih.
const (
	cooldownDenied = 30 * time.Minute
	cooldownBusy   = 2 * time.Minute
)

// Options adalah pengaturan Service.
type Options struct {
	Size    string        // ukuran gambar, mis. 1024x1024
	BaseURL string        // alamat publik middleware, untuk link gambar
	Timeout time.Duration // batas waktu membuat satu gambar
}

// Service menyediakan tool generate_image.
type Service struct {
	gen  Generator
	repo *Repository
	opt  Options
	spec json.RawMessage

	mu        sync.Mutex
	coolUntil map[string]time.Time // akun+model -> dilewati sampai waktu ini
	lists     map[string]modelList // akun -> daftar model gambar
}

// NewService membuat Service.
func NewService(gen Generator, repo *Repository, opt Options) *Service {
	return &Service{gen: gen, repo: repo, opt: opt, coolUntil: map[string]time.Time{}, lists: map[string]modelList{}, spec: llmtool.Function(ToolName,
		"Buat gambar baru dari deskripsi teks (ilustrasi, foto, logo, poster, ikon, dan sejenisnya). "+
			"Pakai hanya bila user meminta dibuatkan gambar. Tulis prompt yang detail dalam bahasa Inggris: "+
			"objek, gaya, warna, komposisi, dan teks yang harus muncul (bila ada). "+
			"Hasilnya berupa URL gambar; tampilkan ke user dengan markdown ![alt](url) persis seperti yang diberikan.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{"type": "string", "description": "deskripsi gambar yang detail, dalam bahasa Inggris"},
				"alt":    map[string]any{"type": "string", "description": "keterangan singkat gambar dalam bahasa user, untuk teks alternatif"},
			},
			"required": []string{"prompt"},
		})}
}

// Enabled melaporkan apakah pembuat gambar aktif.
func (s *Service) Enabled() bool { return s.gen != nil }

// Select menawarkan tool generate_image hanya bila pesan menyinggung gambar
// dan akun 9Router user punya minimal satu model gambar.
func (s *Service) Select(ctx context.Context, query string) []llmtool.Spec {
	if !s.Enabled() || !hasImageIntent(query) {
		return nil
	}
	if models, _ := s.models(ctx); len(models) == 0 {
		return nil
	}
	return []llmtool.Spec{{Name: ToolName, Source: "image", Definition: s.spec}}
}

// Has melaporkan apakah tool ini milik pembuat gambar.
func (s *Service) Has(_ context.Context, name string) bool { return s.Enabled() && name == ToolName }

// Call membuat gambar, menyimpannya, lalu mengembalikan link-nya ke model.
func (s *Service) Call(ctx context.Context, _ string, args string) llmtool.Result {
	var a toolArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(args)), &a); err != nil {
		return llmtool.Result{Text: "argumen tidak valid: kirim {\"prompt\": \"...\"}", IsError: true}
	}
	a.Prompt = strings.TrimSpace(a.Prompt)
	if a.Prompt == "" {
		return llmtool.Result{Text: "prompt kosong: jelaskan gambar yang ingin dibuat", IsError: true}
	}
	if r := []rune(a.Prompt); len(r) > maxPromptRunes {
		a.Prompt = string(r[:maxPromptRunes])
	}

	start := time.Now()
	img, model, err := s.generate(ctx, a.Prompt)
	if err != nil {
		return llmtool.Result{Text: "gagal membuat gambar: " + err.Error() +
			". Sampaikan ke user bahwa layanan gambar sedang tidak tersedia dan sarankan mencoba lagi nanti.", IsError: true}
	}
	name, err := s.repo.Save(img.Data, img.MIME)
	if err != nil {
		logger.Error("simpan gambar gagal", zap.Error(err))
		return llmtool.Result{Text: "gambar dibuat tetapi gagal disimpan: " + err.Error(), IsError: true}
	}

	url := strings.TrimRight(s.opt.BaseURL, "/") + "/images/" + name
	alt := strings.TrimSpace(a.Alt)
	if alt == "" {
		alt = "gambar"
	}
	alt = strings.NewReplacer("[", "", "]", "").Replace(alt)
	latency := time.Since(start)
	logger.Info("gambar dibuat", zap.String("file", name), zap.Int("bytes", len(img.Data)), zap.Duration("latency", latency))

	// catatan di database hanya pelengkap: bila gagal, gambar tetap dikirim ke user
	rec := &GeneratedImage{
		UserID: userctx.From(ctx), URL: url, FileName: name, Prompt: a.Prompt, RevisedPrompt: img.RevisedPrompt, Alt: alt,
		Model: model, MIME: img.MIME, SizeBytes: len(img.Data), LatencyMs: latency.Milliseconds(),
	}
	dbCtx, dbCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer dbCancel()
	if err := s.repo.Create(dbCtx, rec); err != nil {
		logger.Warn("catat gambar ke database gagal", zap.String("file", name), zap.Error(err))
	}

	// model tidak bisa melihat gambar hasilnya; tanpa peringatan ini model
	// cenderung "mendeskripsikan" detail yang sebenarnya hanya isi prompt-nya
	text := "Gambar berhasil dibuat.\nURL: " + url +
		"\nTampilkan ke user dengan markdown persis: ![" + alt + "](" + url + ")" +
		"\nKamu tidak bisa melihat gambar ini. Jangan mengklaim detail isi gambar; cukup sebut gambar dibuat " +
		"sesuai permintaan, lalu tawarkan revisi bila hasilnya belum pas."
	if img.RevisedPrompt != "" {
		text += "\nPrompt yang dipakai: " + img.RevisedPrompt
	}
	return llmtool.Result{Text: text}
}

// generate mencoba model gambar akun user satu per satu sesuai urutan dari 9Router.
// Model yang baru saja menolak dilewati sementara, supaya user tidak menunggu
// model yang pasti gagal di setiap request.
func (s *Service) generate(ctx context.Context, prompt string) (*clients.GeneratedImage, string, error) {
	models, err := s.models(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(models) == 0 {
		return nil, "", errors.New("akun 9Router ini belum punya model gambar; tambahkan provider gambar di dashboard 9Router")
	}
	acct := account(ctx)
	var reasons []string
	for _, model := range models {
		if until, cooling := s.cooling(acct + model); cooling {
			reasons = append(reasons, fmt.Sprintf("%s dilewati sampai %s", model, until.Format("15:04")))
			continue
		}

		attemptCtx, cancel := context.WithTimeout(ctx, s.opt.Timeout)
		img, err := s.gen.Generate(attemptCtx, model, prompt, s.opt.Size)
		cancel()
		if err == nil {
			return img, model, nil
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err() // client memutus koneksi
		}

		wait := s.cooldownFor(err)
		if wait > 0 {
			s.mu.Lock()
			s.coolUntil[acct+model] = time.Now().Add(wait)
			s.mu.Unlock()
		}
		logger.Warn("model gambar gagal, mencoba berikutnya", zap.String("model", model),
			zap.Duration("dilewati", wait), zap.Error(err))
		reasons = append(reasons, model+": "+err.Error())
	}
	return nil, "", errors.New(strings.Join(reasons, "; "))
}

// models mengambil daftar model gambar akun user (dari cache bila masih berlaku).
func (s *Service) models(ctx context.Context) ([]string, error) {
	acct := account(ctx)
	if acct == "" {
		return nil, clients.ErrNoRouterKey
	}
	s.mu.Lock()
	if l, ok := s.lists[acct]; ok && time.Now().Before(l.expires) {
		s.mu.Unlock()
		return l.models, nil
	}
	s.mu.Unlock()

	lctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	models, err := s.gen.ImageModels(lctx)
	ttl := modelsTTL
	if err != nil {
		logger.Warn("daftar model gambar gagal diambil", zap.Error(err))
		ttl = modelsErrTTL
	}
	s.mu.Lock()
	s.lists[acct] = modelList{models: models, expires: time.Now().Add(ttl)}
	s.mu.Unlock()
	return models, err
}

// account mengenali akun 9Router dari hash alamat + key-nya (key asli tidak
// disimpan). Key yang sama di 9Router berbeda dianggap akun berbeda.
func account(ctx context.Context) string {
	key, base := userctx.RouterKey(ctx), userctx.RouterURL(ctx)
	if key == "" || base == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(base + "\x00" + key))
	return hex.EncodeToString(sum[:8]) + "/"
}

func (s *Service) cooling(key string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.coolUntil[key]
	return until, ok && time.Now().Before(until)
}

// cooldownFor menentukan berapa lama model dilewati setelah error ini.
func (s *Service) cooldownFor(err error) time.Duration {
	var ie *clients.ImageError
	if errors.As(err, &ie) {
		switch {
		case ie.Status == 401 || ie.Status == 402 || ie.Status == 403:
			return cooldownDenied
		case ie.Status == 429 || ie.Status >= 500:
			return cooldownBusy
		}
		return 0 // mis. 400: prompt ditolak; model lain mungkin menerima, tidak perlu dilewati
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return cooldownBusy
	}
	return 0
}

// List mengambil catatan gambar yang pernah dibuat.
func (s *Service) List(f ListFilter) ([]GeneratedImage, int64, error) {
	rows, total, err := s.repo.List(f)
	if err != nil {
		logger.Error("baca catatan gambar gagal", zap.Error(err))
		return nil, 0, apperror.NewInternalServerError("Gagal mengambil daftar gambar")
	}
	if rows == nil {
		rows = []GeneratedImage{}
	}
	return rows, total, nil
}

// Get mengambil satu catatan gambar.
func (s *Service) Get(userID, id string) (*GeneratedImage, error) {
	row, err := s.repo.Get(userID, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NewNotFound("Gambar tidak ditemukan")
	}
	return row, err
}

// FilePath mengembalikan lokasi file gambar untuk disajikan.
func (s *Service) FilePath(name string) (string, bool) {
	return s.repo.Path(name)
}

// hasImageIntent melaporkan apakah pesan tampak meminta atau membahas gambar.
func hasImageIntent(text string) bool {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, w := range words {
		if intentWords[w] {
			return true
		}
		for _, p := range intentPrefixes {
			if strings.HasPrefix(w, p) {
				return true
			}
		}
	}
	return false
}
