// Package skillindex membangun index skill di memori dari folder skills/ dan
// memilih skill yang relevan untuk sebuah pesan tanpa LLM: BM25 (kecocokan
// kata) atau embedding (kemiripan makna) bila diaktifkan, dengan pemilih
// cadangan (model) hanya saat skor ambigu.
package skillindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/bm25"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/frontmatter"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/vector"
	"github.com/Bauagg/middleware-skill-ai/internal/services/skillfallback"
)

const (
	skillFile  = "SKILL.md"
	shortFile  = "SHORT.md"
	vectorFile = ".embedding.json" // berawalan titik: diabaikan watcher dan daftar file

	// bobot field saat menyusun teks dokumen BM25: nama skill paling
	// menentukan, lalu keywords, lalu deskripsi
	nameWeight    = 3
	keywordWeight = 2

	// batas bawah "masih mungkin cocok" relatif terhadap threshold: skor di
	// bawahnya dianggap jelas tidak cocok, tanpa perlu bertanya ke model
	bm25LowerFactor = 0.5
	embedLowerGap   = 0.1

	maxClose  = 10 // batas daftar skill berdekatan
	maxRivals = 3  // batas kandidat yang ditawarkan ke pemilih cadangan

	reloadEmbedTimeout = 60 * time.Second
)

// Source adalah sumber data skill (folder di disk). Didefinisikan di sini
// sebagai kebutuhan Service; skillstore.Repository memenuhinya.
type Source interface {
	List() ([]string, error)
	ReadFile(name, file string) ([]byte, error)
	WriteFile(name, file string, data []byte) error
	FileExists(name, file string) bool
	// Signature berubah bila isi folder berubah (dipakai watcher).
	Signature() (string, error)
}

// Embedder mengubah teks menjadi vektor; clients.Embedder memenuhinya.
type Embedder interface {
	Model() string
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

// Fallback memilih skill dengan bantuan model; skillfallback.Service memenuhinya.
type Fallback interface {
	Enabled() bool
	Choose(ctx context.Context, query string, cands []skillfallback.Candidate) (skillfallback.Result, error)
}

// Options mengatur perilaku pemilihan.
type Options struct {
	// BM25
	MinScore       float64 // threshold skor
	SwitchRatio    float64 // skill aktif diganti bila skor skill lain >= SwitchRatio x skor skill aktif
	AmbiguousRatio float64 // skill lain bersaing bila skornya >= AmbiguousRatio x skor teratas

	// embedding (kemiripan kosinus)
	MinSimilarity   float64       // threshold kemiripan
	SwitchMargin    float64       // skill aktif diganti bila kemiripan skill lain > kemiripannya + margin
	AmbiguousMargin float64       // skill lain bersaing bila kemiripannya >= teratas - margin
	EmbedTimeout    time.Duration // batas waktu embedding pesan per request
}

// Deps adalah layanan opsional; nil berarti fitur itu dimatikan.
type Deps struct {
	Embedder Embedder
	Fallback Fallback
}

// Service membangun index dan memilih skill darinya.
type Service struct {
	src  Source
	repo *Repository
	opts Options
	deps Deps

	// mu menyerialkan pembangunan ulang index, supaya reload yang berjalan
	// belakangan selalu membaca disk yang lebih baru. sig adalah sidik jari
	// folder saat index terakhir dibangun.
	mu  sync.Mutex
	sig string
}

// NewService membuat service dengan index kosong. Panggil Reload sekali saat
// start untuk memuat skill yang sudah ada, dan Watch untuk reload otomatis.
func NewService(src Source, repo *Repository, opts Options, deps Deps) *Service {
	return &Service{src: src, repo: repo, opts: opts, deps: deps}
}

// Snapshot mengembalikan isi index saat ini.
func (s *Service) Snapshot() *Snapshot {
	return s.repo.Load()
}

// Version mengidentifikasi versi index saat ini; berubah setiap index dibangun
// ulang. Dipakai sebagai bagian key cache hasil seleksi, supaya cache otomatis
// tidak berlaku lagi setelah skill berubah.
func (s *Service) Version() string {
	snap := s.repo.Load()
	if snap.LoadedAt.IsZero() {
		return "0"
	}
	return strconv.FormatInt(snap.LoadedAt.UnixNano(), 10)
}

// Lookup mencari skill di index berdasarkan nama foldernya.
func (s *Service) Lookup(dir string) (IndexEntry, bool) {
	for _, e := range s.repo.Load().Entries {
		if e.Dir == dir {
			return e, true
		}
	}
	return IndexEntry{}, false
}

// Reload membangun ulang index dari folder skill lalu menukarnya dalam satu
// langkah. Folder yang SKILL.md-nya hilang atau tidak valid dilewati (dicatat
// di log), bukan menggagalkan seluruh reload. Error hanya bila folder skill
// tidak bisa dibaca, dan index lama tetap dipakai.
func (s *Service) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// diambil sebelum membaca isi: perubahan yang terjadi selama reload akan
	// terlihat sebagai sidik jari baru pada pemeriksaan watcher berikutnya
	sig, _ := s.src.Signature()

	dirs, err := s.src.List()
	if err != nil {
		logger.Error("baca folder skill gagal", zap.Error(err))
		return err
	}

	entries := make([]IndexEntry, 0, len(dirs))
	skipped := 0

	for _, dir := range dirs {
		reason := ""

		data, err := s.src.ReadFile(dir, skillFile)
		if err != nil {
			reason = "SKILL.md tidak ditemukan"
		}

		var meta frontmatter.Meta
		if reason == "" {
			if meta, err = frontmatter.Parse(data); err != nil {
				reason = err.Error()
			} else if meta.Description == "" {
				reason = "description kosong"
			}
		}

		if reason != "" {
			skipped++
			logger.Warn("skill dilewati dari index", zap.String("dir", dir), zap.String("reason", reason))
			continue
		}

		if meta.Name == "" {
			meta.Name = dir
		}
		entries = append(entries, IndexEntry{
			Dir:         dir,
			Name:        meta.Name,
			Description: meta.Description,
			Keywords:    meta.Keywords,
			HasShort:    s.src.FileExists(dir, shortFile),
		})
	}

	s.attachVectors(entries)

	snap := newSnapshot(entries)
	s.repo.Replace(snap)
	s.sig = sig
	logger.Info("skill index loaded",
		zap.Int("total", len(entries)), zap.Int("skipped", skipped), zap.Bool("embedding", snap.allVectors))
	return nil
}

// attachVectors mengisi embedding tiap skill. Vektor dibaca dari cache
// .embedding.json di folder skill bila teks dan modelnya sama; sisanya
// dihitung sekaligus lalu disimpan. Bila gagal, skill tetap masuk index dan
// seleksi memakai BM25 sampai reload berikutnya.
func (s *Service) attachVectors(entries []IndexEntry) {
	if s.deps.Embedder == nil || len(entries) == 0 {
		return
	}

	model := s.deps.Embedder.Model()
	var missing []int
	var texts, hashes []string

	for i := range entries {
		text := embedText(entries[i])
		hash := hashText(model, text)
		if v, ok := s.readVector(entries[i].Dir, model, hash); ok {
			entries[i].Vector = v
			continue
		}
		missing = append(missing, i)
		texts = append(texts, text)
		hashes = append(hashes, hash)
	}
	if len(missing) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), reloadEmbedTimeout)
	defer cancel()

	vecs, err := s.deps.Embedder.Embed(ctx, texts)
	if err != nil {
		logger.Warn("hitung embedding skill gagal, seleksi memakai BM25 sampai reload berikutnya",
			zap.Int("skills", len(missing)), zap.Error(err))
		return
	}

	for k, i := range missing {
		entries[i].Vector = vecs[k]
		s.writeVector(entries[i].Dir, vectorCache{Model: model, Hash: hashes[k], Vector: vecs[k]})
	}
	logger.Info("embedding skill dihitung", zap.Int("skills", len(missing)))
}

func (s *Service) readVector(dir, model, hash string) ([]float32, bool) {
	data, err := s.src.ReadFile(dir, vectorFile)
	if err != nil {
		return nil, false
	}
	var c vectorCache
	if json.Unmarshal(data, &c) != nil || c.Model != model || c.Hash != hash || len(c.Vector) == 0 {
		return nil, false
	}
	return c.Vector, true
}

func (s *Service) writeVector(dir string, c vectorCache) {
	data, err := json.Marshal(c)
	if err == nil {
		err = s.src.WriteFile(dir, vectorFile, data)
	}
	if err != nil {
		logger.Warn("simpan cache embedding gagal (akan dihitung ulang saat reload)", zap.String("dir", dir), zap.Error(err))
	}
}

// embedText adalah teks yang di-embed untuk sebuah skill.
func embedText(e IndexEntry) string {
	parts := []string{e.Name, e.Description}
	if len(e.Keywords) > 0 {
		parts = append(parts, strings.Join(e.Keywords, ", "))
	}
	return strings.Join(parts, "\n")
}

func hashText(model, text string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

// Watch memeriksa folder skill tiap interval dan memuat ulang index bila
// isinya berubah (file ditambah, diubah, atau dihapus langsung di disk).
// Perubahan baru dimuat setelah sidik jarinya sama pada dua pemeriksaan
// berturut-turut, supaya file yang sedang ditulis tidak terbaca setengah.
// Berhenti saat ctx selesai; interval <= 0 berarti tidak melakukan apa pun.
func (s *Service) Watch(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var pending string // sidik jari perubahan yang sedang menunggu stabil
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		sig, err := s.src.Signature()
		if err != nil {
			logger.Warn("periksa folder skill gagal", zap.Error(err))
			continue
		}

		s.mu.Lock()
		applied := s.sig
		s.mu.Unlock()

		switch {
		case sig == applied:
			pending = ""
		case sig != pending:
			pending = sig // baru terlihat berubah; tunggu satu interval lagi
		default:
			logger.Info("perubahan folder skill terdeteksi, memuat ulang index")
			_ = s.Reload()
			pending = ""
		}
	}
}

// Select memilih skill untuk sebuah pesan:
//  1. skill diurutkan dengan embedding (bila aktif) atau BM25;
//  2. skor teratas jelas tinggi dan tanpa saingan: langsung dipilih;
//  3. skor ambigu (dua skill bersaing ketat, atau teratas sedikit di bawah
//     threshold): pemilih cadangan (model) memutuskan, bila aktif;
//  4. selain itu: dipilih bila skor teratas mencapai threshold.
func (s *Service) Select(ctx context.Context, query string) Selection {
	if strings.TrimSpace(query) == "" {
		return Selection{}
	}

	ranked, method, degraded := s.rank(ctx, s.repo.Load(), query)
	a := s.analyze(ranked, method)

	sel := Selection{Method: method, Close: a.close, Degraded: degraded}
	if a.top == nil {
		return sel
	}

	if a.ambiguous && s.fallbackEnabled() {
		res, err := s.deps.Fallback.Choose(ctx, query, toCandidates(a.rivals))
		sel.FallbackTokens = res.Tokens
		if err == nil {
			sel.Method = MethodFallback
			sel.Best = findMatch(a.rivals, res.Dir) // nil bila model menjawab "none"
			return sel
		}
		sel.Degraded = true
		logger.Warn("pemilih cadangan gagal, memakai keputusan dari skor", zap.Error(err))
	}

	if a.top.Score >= a.min {
		sel.Best = a.top
	}
	return sel
}

// Choose menimbang hasil seleksi terhadap skill aktif percakapan (activeDir,
// "" bila belum ada):
//   - tanpa skill aktif: hasil seleksi dipakai apa adanya;
//   - pesan lanjutan yang tidak cocok skill mana pun: skill aktif dipertahankan
//     (isi skill harus dikirim ulang di setiap request, karena model tidak
//     menyimpan apa pun antar request);
//   - skill aktif masih termasuk skill yang skornya berdekatan dengan skill
//     teratas: dipertahankan, karena skill lain tidak "jauh lebih cocok";
//   - selain itu: diganti dengan hasil seleksi.
func (s *Service) Choose(sel Selection, activeDir string) (*Match, Decision) {
	if activeDir == "" {
		return sel.Best, DecisionNone
	}

	active, ok := s.Lookup(activeDir)
	if !ok {
		return sel.Best, DecisionNone // skill aktif sudah dihapus
	}

	keep := &Match{Entry: active}
	if m := findMatch(sel.Close, activeDir); m != nil {
		keep.Score = m.Score
	}

	switch {
	case sel.Best == nil:
		return keep, DecisionKeep
	case sel.Best.Entry.Dir == activeDir:
		return sel.Best, DecisionKeep
	case findMatch(sel.Close, activeDir) != nil:
		return keep, DecisionKeep
	default:
		return sel.Best, DecisionSwitch
	}
}

// Match menampilkan kandidat beserta skornya dan skill yang akan terpilih,
// untuk memeriksa hasil pemilihan dan menyetel threshold. Pemilih cadangan
// hanya dijalankan bila withFallback (memakai token).
func (s *Service) Match(ctx context.Context, query string, limit int, withFallback bool) *MatchResult {
	ranked, method, _ := s.rank(ctx, s.repo.Load(), query)
	a := s.analyze(ranked, method)

	res := &MatchResult{
		Query:           query,
		Method:          method,
		Threshold:       s.policyFor(method).min,
		Ambiguous:       a.ambiguous,
		FallbackEnabled: s.fallbackEnabled(),
		Candidates:      make([]MatchCandidate, 0, limit),
	}
	for i, m := range ranked {
		if i >= limit {
			break
		}
		res.Candidates = append(res.Candidates, MatchCandidate{
			Name:        m.Entry.Name,
			Description: m.Entry.Description,
			Score:       math.Round(m.Score*1000) / 1000,
		})
	}

	if a.top != nil && a.top.Score >= a.min {
		res.Selected = &a.top.Entry.Name
	}

	if withFallback {
		sel := s.Select(ctx, query)
		res.Method, res.FallbackTokens, res.Selected = sel.Method, sel.FallbackTokens, nil
		if sel.Best != nil {
			res.Selected = &sel.Best.Entry.Name
		}
	}
	return res
}

func (s *Service) fallbackEnabled() bool {
	return s.deps.Fallback != nil && s.deps.Fallback.Enabled()
}

// rank mengurutkan semua skill untuk query, dengan embedding bila aktif dan
// semua skill punya vektor, selain itu BM25. degraded true bila embedding
// seharusnya dipakai tetapi gagal.
func (s *Service) rank(ctx context.Context, snap *Snapshot, query string) (ranked []Match, method string, degraded bool) {
	if s.deps.Embedder != nil && snap.allVectors {
		ectx, cancel := context.WithTimeout(ctx, s.opts.EmbedTimeout)
		vecs, err := s.deps.Embedder.Embed(ectx, []string{query})
		cancel()
		if err == nil && len(vecs) == 1 {
			return rankByVector(snap, vecs[0]), MethodEmbedding, false
		}
		logger.Warn("embedding pesan gagal, seleksi memakai BM25", zap.Error(err))
		degraded = true
	}
	return search(snap, query, 0), MethodBM25, degraded
}

// policy adalah aturan skor untuk satu metode seleksi, karena skala skor BM25
// dan kemiripan kosinus berbeda.
type policy struct {
	min   float64 // threshold
	lower float64 // di bawah ini jelas tidak cocok
	close func(top, score float64) bool
	rival func(top, score float64) bool
}

func (s *Service) policyFor(method string) policy {
	o := s.opts
	if method == MethodEmbedding {
		return policy{
			min:   o.MinSimilarity,
			lower: o.MinSimilarity - embedLowerGap,
			close: func(top, v float64) bool { return v > top-o.SwitchMargin },
			rival: func(top, v float64) bool { return v >= top-o.AmbiguousMargin },
		}
	}
	return policy{
		min:   o.MinScore,
		lower: o.MinScore * bm25LowerFactor,
		close: func(top, v float64) bool { return top < v*o.SwitchRatio },
		rival: func(top, v float64) bool { return v >= top*o.AmbiguousRatio },
	}
}

// analysis adalah hasil membaca urutan skor: skill teratas, skill yang
// berdekatan, saingan, dan apakah skornya ambigu.
type analysis struct {
	top       *Match
	close     []Match
	rivals    []Match
	ambiguous bool
	min       float64
}

func (s *Service) analyze(ranked []Match, method string) analysis {
	p := s.policyFor(method)
	a := analysis{min: p.min}
	if len(ranked) == 0 || ranked[0].Score < p.lower {
		return a
	}

	top := ranked[0]
	a.top = &top
	for _, m := range ranked {
		if m.Score < p.lower {
			break // urutan menurun: sisanya jelas tidak cocok
		}
		if len(a.close) < maxClose && p.close(top.Score, m.Score) {
			a.close = append(a.close, m)
		}
		if len(a.rivals) < maxRivals && p.rival(top.Score, m.Score) {
			a.rivals = append(a.rivals, m)
		}
	}

	a.ambiguous = len(a.rivals) >= 2 || top.Score < p.min
	return a
}

func toCandidates(matches []Match) []skillfallback.Candidate {
	out := make([]skillfallback.Candidate, 0, len(matches))
	for _, m := range matches {
		out = append(out, skillfallback.Candidate{Dir: m.Entry.Dir, Name: m.Entry.Name, Description: m.Entry.Description})
	}
	return out
}

func findMatch(matches []Match, dir string) *Match {
	if dir == "" {
		return nil
	}
	for i := range matches {
		if matches[i].Entry.Dir == dir {
			m := matches[i]
			return &m
		}
	}
	return nil
}

// newSnapshot membangun snapshot beserta statistik BM25-nya. Teks dokumen
// adalah gabungan nama, keywords, dan deskripsi dengan bobot di atas.
func newSnapshot(entries []IndexEntry) *Snapshot {
	docs := make([][]string, len(entries))
	allVectors := len(entries) > 0
	for i, e := range entries {
		var tokens []string
		for range nameWeight {
			tokens = append(tokens, bm25.Tokenize(e.Name)...)
		}
		for range keywordWeight {
			tokens = append(tokens, bm25.Tokenize(strings.Join(e.Keywords, " "))...)
		}
		tokens = append(tokens, bm25.Tokenize(e.Description)...)
		docs[i] = tokens

		if len(e.Vector) == 0 {
			allVectors = false
		}
	}

	return &Snapshot{Entries: entries, LoadedAt: time.Now(), corpus: bm25.New(docs), allVectors: allVectors}
}

// search mengurutkan skill dengan BM25, dari skor tertinggi. Hanya skill
// dengan skor > 0 yang dikembalikan; limit <= 0 berarti semua.
func search(snap *Snapshot, query string, limit int) []Match {
	if snap.corpus == nil {
		return nil
	}

	var matches []Match
	for i, score := range snap.corpus.Score(bm25.Tokenize(query)) {
		if score > 0 {
			matches = append(matches, Match{Entry: snap.Entries[i], Score: score})
		}
	}
	sortMatches(matches)

	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches
}

// rankByVector mengurutkan skill dengan kemiripan kosinus terhadap vektor pesan.
func rankByVector(snap *Snapshot, q []float32) []Match {
	matches := make([]Match, 0, len(snap.Entries))
	for _, e := range snap.Entries {
		matches = append(matches, Match{Entry: e, Score: vector.Cosine(q, e.Vector)})
	}
	sortMatches(matches)
	return matches
}

func sortMatches(matches []Match) {
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Entry.Name < matches[j].Entry.Name
	})
}
