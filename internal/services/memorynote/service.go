// Package memorynote mengelola catatan memori (Tahap 4): pengalaman yang
// pernah dicoba beserta hasilnya ("dicoba X, hasilnya Y, pelajarannya Z").
//
// Alurnya:
//  1. setelah respons chat selesai, pesan user yang memuat sinyal hasil
//     (berhasil, gagal, omzet naik, ...) diekstrak model di latar belakang
//     menjadi catatan berstatus "proposed";
//  2. catatan dikonfirmasi, dikoreksi, atau ditolak lewat API;
//  3. catatan aktif yang cocok dengan pesan user (maks. beberapa) disisipkan
//     ke prompt sebagai pengingat, bukan larangan.
//
// Memori ini global: tidak terikat user mana pun.
package memorynote

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
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/bm25"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/tokens"
)

const (
	// candidateLimit: berapa kandidat diambil dari pencarian teks sebelum
	// disaring jumlah kata yang cocok.
	candidateLimit = 10

	// maxQueryTerms membatasi jumlah kata pesan yang dipakai mencari.
	maxQueryTerms = 30

	// batas teks yang dikirim ke model ekstraksi dan yang disimpan
	excerptRunes    = 500
	turnRunes       = 1500
	maxExtractTurns = 6
	maxNotesPerMsg  = 3
	fieldRunes      = 500

	// minSignalRunes: pesan yang terlalu pendek ("berhasil!") tidak cukup
	// untuk dijadikan catatan.
	minSignalRunes = 20
)

// resultSignals adalah kata yang menandakan user melaporkan hasil. Ekstraksi
// hanya dijalankan bila salah satunya muncul, supaya model tidak dipanggil di
// setiap pesan.
var resultSignals = toSet(
	// indonesia
	"hasil", "hasilnya", "berhasil", "gagal", "omzet", "omset", "laku", "laris",
	"naik", "turun", "untung", "rugi", "boncos", "terjual", "penjualan", "closing",
	"konversi", "meningkat", "menurun", "anjlok", "melonjak", "sepi", "ramai",
	"profit", "margin", "balik", "efektif", "zonk", "flop", "viral",
	// inggris
	"worked", "failed", "revenue", "sales", "sold", "conversion", "increased",
	"decreased", "dropped", "grew", "growth", "roi", "roas", "ctr",
)

// Completer memanggil model chat; clients.NineRouter memenuhinya.
type Completer interface {
	Complete(ctx context.Context, model string, messages []clients.ChatMessage) (*clients.Completion, error)
}

// Options adalah pengaturan Service.
type Options struct {
	MaxNotes      int           // catatan maksimal yang disisipkan per request
	NoteMaxTokens int           // batas token per catatan di prompt
	MinMatch      int           // kata pesan minimal yang harus ada di catatan
	LookupTimeout time.Duration // batas waktu pencarian catatan per request
	NoteTTL       time.Duration // masa berlaku catatan sejak dikonfirmasi (0 = selamanya)

	ExtractModel       string        // model ekstraksi ("" = ekstraksi dimatikan)
	ExtractTimeout     time.Duration // batas waktu satu ekstraksi
	ExtractConcurrency int           // ekstraksi yang boleh berjalan bersamaan
}

// Service mengelola catatan memori.
type Service struct {
	repo *Repository
	llm  Completer // boleh nil bila ekstraksi dimatikan
	opt  Options

	sem      chan struct{} // membatasi ekstraksi yang berjalan bersamaan
	inflight sync.Map      // hash pesan yang sedang diekstrak
}

// NewService membuat Service.
func NewService(repo *Repository, llm Completer, opt Options) *Service {
	if opt.ExtractConcurrency < 1 {
		opt.ExtractConcurrency = 1
	}
	return &Service{repo: repo, llm: llm, opt: opt, sem: make(chan struct{}, opt.ExtractConcurrency)}
}

// ExtractEnabled melaporkan apakah ekstraksi otomatis aktif.
func (s *Service) ExtractEnabled() bool {
	return s.llm != nil && s.opt.ExtractModel != ""
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

// List mengambil atau mencari catatan.
func (s *Service) List(f ListFilter) ([]MemoryNote, int64, error) {
	tsq := ""
	if q := strings.TrimSpace(f.Query); q != "" {
		terms := queryTerms(q)
		if len(terms) == 0 {
			return []MemoryNote{}, 0, nil // hanya kata umum: tidak ada yang dicari
		}
		tsq = tsQuery(terms)
	}

	rows, total, err := s.repo.List(f, tsq)
	if err != nil {
		logger.Error("baca catatan memori gagal", zap.Error(err))
		return nil, 0, apperror.NewInternalServerError("Gagal mengambil catatan memori")
	}
	if rows == nil {
		rows = []MemoryNote{}
	}
	return rows, total, nil
}

// Get mengambil satu catatan (yang sudah dihapus dianggap tidak ada).
func (s *Service) Get(id string) (*MemoryNote, error) {
	row, err := s.repo.Get(id)
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && row.Status == StatusDeleted) {
		return nil, apperror.NewNotFound("Catatan memori tidak ditemukan")
	}
	return row, err
}

// Create menyimpan catatan manual; langsung aktif karena ditulis sendiri.
func (s *Service) Create(in CreateInput) (*MemoryNote, error) {
	now := time.Now()
	row := &MemoryNote{
		Topic:        clean(in.Topic),
		Tried:        clean(in.Tried),
		Result:       clean(in.Result),
		ResultSource: orDefault(in.ResultSource, SourceUser),
		Lesson:       clean(in.Lesson),
		Status:       StatusActive,
		Origin:       OriginManual,
		ConfirmedAt:  &now,
		ExpiresAt:    s.expiry(now),
	}
	row.NoteHash = noteHash(row.Topic, row.Tried, row.Result)

	if err := s.repo.Create(row); err != nil {
		return nil, err // catatan yang sama persis dipetakan apperror.Handle menjadi 409
	}
	return row, nil
}

// Confirm mengaktifkan catatan usulan (atau yang pernah ditolak).
func (s *Service) Confirm(id string) (*MemoryNote, error) {
	return s.transition(id, StatusActive, StatusProposed, StatusRejected)
}

// Reject menolak catatan; catatan yang sama tidak akan diusulkan lagi.
func (s *Service) Reject(id string) (*MemoryNote, error) {
	return s.transition(id, StatusRejected, StatusProposed, StatusActive, StatusCorrected)
}

// Delete menghapus catatan. Barisnya tetap disimpan berstatus deleted supaya
// ekstraksi tidak mengusulkan catatan yang sama lagi.
func (s *Service) Delete(id string) error {
	if _, err := s.Get(id); err != nil {
		return err
	}
	return s.repo.Update(id, map[string]any{"status": StatusDeleted})
}

// Correct memperbaiki isi catatan. Catatan yang dikoreksi dianggap sudah
// diperiksa manusia, jadi langsung dipakai (status corrected).
func (s *Service) Correct(id string, in CorrectInput) (*MemoryNote, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}

	fields := map[string]any{}
	set := func(col string, v *string) {
		if v != nil {
			fields[col] = clean(*v)
		}
	}
	set("topic", in.Topic)
	set("tried", in.Tried)
	set("result", in.Result)
	set("result_source", in.ResultSource)
	set("lesson", in.Lesson)
	if len(fields) == 0 {
		return nil, apperror.NewBadRequest("Tidak ada field yang diubah")
	}

	now := time.Now()
	fields["status"] = StatusCorrected
	fields["confirmed_at"] = now
	fields["expires_at"] = s.expiry(now)
	if err := s.repo.Update(id, fields); err != nil {
		return nil, err
	}
	return s.Get(id)
}

// transition mengubah status catatan bila status sekarang termasuk from.
func (s *Service) transition(id, to string, from ...string) (*MemoryNote, error) {
	row, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if !contains(from, row.Status) {
		return nil, apperror.NewBadRequest(fmt.Sprintf("Catatan berstatus %s tidak bisa diubah menjadi %s", row.Status, to))
	}

	fields := map[string]any{"status": to}
	if to == StatusActive {
		now := time.Now()
		fields["confirmed_at"] = now
		fields["expires_at"] = s.expiry(now)
	}
	if err := s.repo.Update(id, fields); err != nil {
		return nil, err
	}
	return s.Get(id)
}

// Match menampilkan catatan yang akan disisipkan untuk pesan q (untuk
// memeriksa memori tanpa mengirim chat).
func (s *Service) Match(ctx context.Context, q string) (*MatchResult, error) {
	hits, err := s.search(ctx, q)
	if err != nil {
		logger.Error("cari catatan memori gagal", zap.Error(err))
		return nil, apperror.NewInternalServerError("Gagal mencari catatan memori")
	}
	res := &MatchResult{Query: q, Notes: hits}
	if len(hits) > 0 {
		res.Block = s.Block(hits)
		res.TokensEst = tokens.Estimate(res.Block)
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// dipakai proxy
// ---------------------------------------------------------------------------

// Relevant mengambil catatan yang cocok dengan pesan user untuk disisipkan.
// Fail-open: bila database lambat atau gagal, request tetap jalan tanpa catatan.
func (s *Service) Relevant(ctx context.Context, query string) []Hit {
	if s.opt.MaxNotes < 1 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.opt.LookupTimeout)
	defer cancel()

	hits, err := s.search(ctx, query)
	if err != nil {
		logger.Warn("cari catatan memori gagal, request diteruskan tanpa catatan", zap.Error(err))
		return nil
	}
	return hits
}

// search mencari kandidat dengan pencarian teks PostgreSQL, lalu menyaring
// yang memuat cukup banyak kata pesan. Pencarian prefix bisa terlalu longgar
// ("naik:*" juga cocok "naikkan"), jadi penghitungan ulang dengan tokenizer
// yang sama dengan seleksi skill menjadi penentu akhirnya.
func (s *Service) search(ctx context.Context, query string) ([]Hit, error) {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return []Hit{}, nil
	}

	cands, err := s.repo.Search(ctx, tsQuery(terms), candidateLimit)
	if err != nil {
		return nil, err
	}

	need := min(max(s.opt.MinMatch, 1), len(terms))
	out := make([]Hit, 0, s.opt.MaxNotes)
	for _, h := range cands {
		h.Matched = countMatches(terms, h.Topic+" "+h.Tried+" "+h.Result+" "+h.Lesson)
		if h.Matched < need {
			continue
		}
		out = append(out, h)
		if len(out) == s.opt.MaxNotes {
			break
		}
	}
	return out, nil
}

// Block menyusun catatan menjadi teks yang disisipkan ke system prompt.
func (s *Service) Block(hits []Hit) string {
	var b strings.Builder
	b.WriteString("<memory_notes>\n")
	b.WriteString("Catatan pengalaman sebelumnya. Ini pengingat yang perlu dicek ulang, bukan larangan: " +
		"kondisi bisa sudah berubah. Bila rencana user mengulang pola yang dulu gagal, sebutkan " +
		"catatan itu lebih dulu, lalu tawarkan alternatif. Jangan menyebut catatan yang tidak relevan.\n")
	for _, h := range hits {
		line := fmt.Sprintf("- [%s, dicatat %s] Dicoba: %s | Hasil: %s (sumber: %s) | Pelajaran: %s",
			h.Topic, h.CreatedAt.Format("2006-01-02"), h.Tried, h.Result, h.ResultSource, h.Lesson)
		if s.opt.NoteMaxTokens > 0 {
			if cut, ok := tokens.Truncate(line, s.opt.NoteMaxTokens); ok {
				line = cut + " ..."
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("</memory_notes>")
	return b.String()
}

// Observe dipanggil setelah respons chat selesai. Bila pesan user memuat
// sinyal hasil, ekstraksi dijalankan di latar belakang; tidak pernah menunda
// request. Pesan yang sama (request diulang) tidak diekstrak dua kali.
func (s *Service) Observe(userText string, recent []Turn) {
	if !s.ExtractEnabled() || !hasResultSignal(userText) {
		return
	}

	hash := sourceHash(userText)
	if _, busy := s.inflight.LoadOrStore(hash, struct{}{}); busy {
		return
	}

	select {
	case s.sem <- struct{}{}:
	default:
		s.inflight.Delete(hash)
		logger.Warn("ekstraksi memori dilewati: antrean penuh")
		return
	}

	go func() {
		defer func() {
			<-s.sem
			s.inflight.Delete(hash)
			if r := recover(); r != nil {
				logger.Error("ekstraksi memori panic", zap.Any("panic", r))
			}
		}()
		s.extract(userText, hash, recent)
	}()
}

// extract memanggil model untuk mengubah pesan user menjadi catatan usulan.
func (s *Service) extract(userText, hash string, recent []Turn) {
	ctx, cancel := context.WithTimeout(context.Background(), s.opt.ExtractTimeout)
	defer cancel()

	seen, err := s.repo.SourceSeen(ctx, hash)
	if err != nil {
		logger.Warn("cek sumber catatan gagal", zap.Error(err))
		return
	}
	if seen {
		return
	}

	start := time.Now()
	resp, err := s.llm.Complete(ctx, s.opt.ExtractModel, extractPrompt(userText, recent))
	if err != nil {
		logger.Warn("ekstraksi memori gagal", zap.String("model", s.opt.ExtractModel), zap.Error(err))
		return
	}

	items, err := parseExtraction(resp.Text)
	if err != nil {
		logger.Warn("jawaban ekstraksi memori tidak bisa dibaca", zap.Error(err))
		return
	}

	rows := make([]MemoryNote, 0, len(items))
	excerpt := clip(userText, excerptRunes)
	for _, it := range items {
		rows = append(rows, MemoryNote{
			Topic:         it.Topic,
			Tried:         it.Tried,
			Result:        it.Result,
			ResultSource:  it.ResultSource,
			Lesson:        it.Lesson,
			Status:        StatusProposed,
			Origin:        OriginExtract,
			SourceExcerpt: excerpt,
			SourceHash:    hash,
			NoteHash:      noteHash(it.Topic, it.Tried, it.Result),
			ExtractModel:  s.opt.ExtractModel,
		})
	}

	saved, err := s.repo.CreateIgnore(ctx, rows)
	if err != nil {
		logger.Error("simpan catatan memori gagal", zap.Error(err))
		return
	}

	fields := []zap.Field{
		zap.Int("found", len(items)), zap.Int64("saved", saved),
		zap.Int64("latency_ms", time.Since(start).Milliseconds()),
	}
	if u := resp.Usage; u != nil {
		fields = append(fields, zap.Int("prompt_tokens", u.PromptTokens), zap.Int("completion_tokens", u.CompletionTokens))
	}
	logger.Info("ekstraksi memori selesai", fields...)
}

// extractPrompt menyusun instruksi ekstraksi. Model hanya boleh mencatat hal
// yang benar-benar sudah dicoba dan hasilnya sudah terjadi, bukan rencana
// atau saran asisten.
func extractPrompt(userText string, recent []Turn) []clients.ChatMessage {
	system := `Kamu mengekstrak catatan pengalaman bisnis dari percakapan.
Catat HANYA hal yang user laporkan sudah benar-benar dicoba DAN hasilnya sudah terjadi.
Jangan catat rencana, pertanyaan, hipotesis, atau saran dari asisten.
Bila tidak ada, jawab {"notes":[]}.

Jawab hanya JSON:
{"notes":[{"topic":"...","tried":"...","result":"...","result_source":"user|data","lesson":"..."}]}

- topic: 2-6 kata, inti topiknya (mis. "promo diskon produk A").
- tried: apa yang dicoba, spesifik.
- result: apa yang terjadi, sertakan angka bila ada.
- result_source: "data" bila hasilnya disertai angka/data terukur, "user" bila hanya klaim atau kesan user.
- lesson: satu kalimat pelajaran yang bisa dipakai lagi; netral, bukan larangan.
- maksimal 3 catatan, tiap field maksimal 300 karakter, pakai bahasa yang sama dengan user.`

	var b strings.Builder
	if len(recent) > 0 {
		b.WriteString("Percakapan sebelumnya (konteks):\n")
		for _, t := range recent {
			fmt.Fprintf(&b, "[%s] %s\n", t.Role, clip(t.Text, turnRunes))
		}
		b.WriteString("\n")
	}
	b.WriteString("Pesan terakhir user (sumber catatan):\n")
	b.WriteString(clip(userText, turnRunes*2))

	return []clients.ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: b.String()},
	}
}

// parseExtraction membaca JSON catatan dari jawaban model, walau dibungkus
// teks lain atau blok kode. Catatan yang field wajibnya kosong dibuang.
func parseExtraction(text string) ([]extracted, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("bukan JSON: %q", clip(text, 120))
	}

	var v struct {
		Notes []extracted `json:"notes"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &v); err != nil {
		return nil, fmt.Errorf("JSON tidak valid: %w", err)
	}

	out := make([]extracted, 0, len(v.Notes))
	for _, n := range v.Notes {
		n.Topic = clip(clean(n.Topic), 200)
		n.Tried = clip(clean(n.Tried), fieldRunes)
		n.Result = clip(clean(n.Result), fieldRunes)
		n.Lesson = clip(clean(n.Lesson), fieldRunes)
		if n.ResultSource != SourceData {
			n.ResultSource = SourceUser
		}
		if n.Topic == "" || n.Tried == "" || n.Result == "" || n.Lesson == "" {
			continue
		}
		out = append(out, n)
		if len(out) == maxNotesPerMsg {
			break
		}
	}
	return out, nil
}

// expiry menghitung kapan catatan kedaluwarsa (nil = tanpa batas).
func (s *Service) expiry(from time.Time) *time.Time {
	if s.opt.NoteTTL <= 0 {
		return nil
	}
	t := from.Add(s.opt.NoteTTL)
	return &t
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

// hasResultSignal melaporkan apakah pesan tampak melaporkan sebuah hasil.
func hasResultSignal(text string) bool {
	if len([]rune(strings.TrimSpace(text))) < minSignalRunes {
		return false
	}
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, w := range words {
		if resultSignals[w] {
			return true
		}
	}
	return false
}

// queryTerms mengambil kata isi unik dari pesan (tanpa kata umum).
func queryTerms(q string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, t := range bm25.Tokenize(q) {
		if seen[t] {
			continue
		}
		seen[t] = true
		terms = append(terms, t)
		if len(terms) == maxQueryTerms {
			break
		}
	}
	return terms
}

// countMatches menghitung berapa kata query yang ada di teks.
func countMatches(terms []string, text string) int {
	have := map[string]bool{}
	for _, t := range bm25.Tokenize(text) {
		have[t] = true
	}
	n := 0
	for _, t := range terms {
		if have[t] {
			n++
		}
	}
	return n
}

// sourceHash adalah sidik pesan user (spasi dan huruf besar diabaikan).
func sourceHash(text string) string {
	return sha(strings.ToLower(strings.Join(strings.Fields(text), " ")))
}

// noteHash adalah sidik isi catatan untuk mencegah duplikat.
func noteHash(topic, tried, result string) string {
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	return sha(norm(topic) + "\n" + norm(tried) + "\n" + norm(result))
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// clean merapikan spasi.
func clean(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clip memotong teks sampai n karakter.
func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "..."
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func toSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
