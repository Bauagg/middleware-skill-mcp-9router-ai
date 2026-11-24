// Package eval menguji kualitas skill: pertanyaan uji di
// skills/<nama>/EVAL.yaml dijawab model dengan beberapa varian skill (SKILL.md
// lengkap, SHORT.md, dan tanpa skill sebagai pembanding), lalu tiap jawaban
// dinilai model hakim (0-10) berdasarkan kriteria per pertanyaan. Hasilnya
// disimpan di t_eval_runs, supaya bisa diputuskan misalnya apakah SHORT.md
// cukup setara untuk dipakai (lebih hemat token).
package eval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/tokens"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
	"github.com/Bauagg/middleware-skill-ai/internal/services/skillstore"
)

const (
	evalFileName = "EVAL.yaml"
	skillFile    = "SKILL.md"
	shortFile    = "SHORT.md"

	// VariantNone menjawab tanpa skill, sebagai pembanding.
	VariantNone = "none"

	maxCases    = 30
	callTimeout = 3 * time.Minute

	// equalTolerance: selisih skor rata-rata yang masih dianggap "setara".
	equalTolerance = 0.5
)

// Completer memanggil model chat; clients.NineRouter memenuhinya.
type Completer interface {
	Complete(ctx context.Context, model string, messages []clients.ChatMessage) (*clients.Completion, error)
}

// Renderer menyiapkan teks varian skill; skillstore.Service memenuhinya.
type Renderer interface {
	RenderVariant(dir, variant string) (*skillstore.Rendered, error)
}

// Files membaca file di folder skill; skillstore.Repository memenuhinya.
type Files interface {
	Exists(name string) bool
	FileExists(name, file string) bool
	ReadFile(name, file string) ([]byte, error)
}

// Store menyimpan hasil eval; Repository memenuhinya.
type Store interface {
	Save(row *EvalRun) error
	ByRun(userID, runID string) ([]EvalRun, error)
	Runs(userID, skill string, limit int) ([]RunListItem, error)
}

// Deps adalah semua yang dibutuhkan Service.
type Deps struct {
	LLM         Completer
	Renderer    Renderer
	Files       Files
	Store       Store
	Concurrency int // berapa pertanyaan diproses bersamaan
}

// Service menjalankan dan meringkas eval.
type Service struct {
	Deps

	mu     sync.Mutex
	active map[string]*progress // eval yang sedang berjalan, per run_id
}

type progress struct {
	total, done int
}

// job adalah satu pertanyaan x satu varian.
type job struct {
	runID, skill, version, model, userID string
	judgeModel, routerKey, routerURL     string
	index                                int
	c                                    Case
	variant                              string
	rendered                             *skillstore.Rendered // nil untuk "none"
}

// NewService membuat service eval.
func NewService(deps Deps) *Service {
	if deps.Concurrency < 1 {
		deps.Concurrency = 1
	}
	return &Service{Deps: deps, active: map[string]*progress{}}
}

// Start memvalidasi permintaan lalu menjalankan eval di latar belakang.
// Hasilnya dipantau lewat Summary dengan run_id yang dikembalikan.
func (s *Service) Start(in RunInput) (*RunStarted, error) {
	skill := strings.TrimSpace(in.Skill)
	if skill == "" || skill != filepath.Base(skill) || strings.HasPrefix(skill, ".") {
		return nil, apperror.NewBadRequest("Validasi gagal", response.FieldError{Field: "skill", Message: "nama skill tidak valid"})
	}
	if !s.Files.Exists(skill) {
		return nil, apperror.NewNotFound("Skill tidak ditemukan")
	}

	cases, err := s.loadCases(skill)
	if err != nil {
		return nil, err
	}
	if in.Limit > 0 && in.Limit < len(cases) {
		cases = cases[:in.Limit]
	}

	variants, err := s.resolveVariants(skill, in.Variants)
	if err != nil {
		return nil, err
	}

	model := strings.TrimSpace(in.Model)
	if model == "" {
		return nil, apperror.NewBadRequest("Validasi gagal",
			response.FieldError{Field: "model", Message: "model wajib diisi dengan model 9Router milikmu"})
	}
	judgeModel := strings.TrimSpace(in.JudgeModel)
	if judgeModel == "" {
		judgeModel = model
	}

	rendered := map[string]*skillstore.Rendered{}
	for _, v := range variants {
		if v == VariantNone {
			continue
		}
		r, err := s.Renderer.RenderVariant(skill, v)
		if err != nil {
			return nil, apperror.NewBadRequest("Skill tidak bisa disiapkan", response.FieldError{Field: "variants", Message: err.Error()})
		}
		rendered[v] = r
	}

	runID, err := newUUID()
	if err != nil {
		return nil, apperror.NewInternalServerError("Gagal membuat run_id")
	}
	version := s.skillVersion(skill)

	var jobs []job
	for i, c := range cases {
		for _, v := range variants {
			jobs = append(jobs, job{runID: runID, userID: in.UserID, routerKey: in.RouterKey, routerURL: in.RouterURL, skill: skill, version: version, model: model, judgeModel: judgeModel, index: i, c: c, variant: v, rendered: rendered[v]})
		}
	}

	s.mu.Lock()
	s.active[runID] = &progress{total: len(jobs)}
	s.mu.Unlock()

	go s.run(runID, jobs)

	logger.Info("eval dimulai", zap.String("run_id", runID), zap.String("skill", skill), zap.Int("jobs", len(jobs)))
	return &RunStarted{
		RunID: runID, Skill: skill, SkillVersion: version, Variants: variants,
		Cases: len(cases), Jobs: len(jobs), Model: model, JudgeModel: judgeModel,
	}, nil
}

// loadCases membaca dan memvalidasi EVAL.yaml.
func (s *Service) loadCases(skill string) ([]Case, error) {
	raw, err := s.Files.ReadFile(skill, evalFileName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, apperror.NewBadRequest("Pertanyaan uji belum ada", response.FieldError{
				Field:   "skill",
				Message: fmt.Sprintf("buat skills/%s/%s berisi cases: [{question: ..., criteria: ...}]", skill, evalFileName),
			})
		}
		return nil, apperror.NewInternalServerError("Gagal membaca " + evalFileName)
	}

	var f evalFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, apperror.NewBadRequest(evalFileName+" tidak valid", response.FieldError{Field: "skill", Message: err.Error()})
	}

	cases := make([]Case, 0, len(f.Cases))
	for i, c := range f.Cases {
		c.Question, c.Criteria = strings.TrimSpace(c.Question), strings.TrimSpace(c.Criteria)
		if c.Question == "" || c.Criteria == "" {
			return nil, apperror.NewBadRequest(evalFileName+" tidak valid", response.FieldError{
				Field: "skill", Message: fmt.Sprintf("pertanyaan ke-%d wajib punya question dan criteria", i+1),
			})
		}
		cases = append(cases, c)
	}

	if len(cases) == 0 || len(cases) > maxCases {
		return nil, apperror.NewBadRequest(evalFileName+" tidak valid", response.FieldError{
			Field: "skill", Message: fmt.Sprintf("jumlah pertanyaan harus 1-%d (disarankan 10-20), ada %d", maxCases, len(cases)),
		})
	}
	return cases, nil
}

// resolveVariants menentukan varian yang dibandingkan.
func (s *Service) resolveVariants(skill string, requested []string) ([]string, error) {
	hasShort := s.Files.FileExists(skill, shortFile)

	if len(requested) == 0 {
		v := []string{skillstore.VariantFull}
		if hasShort {
			v = append(v, skillstore.VariantShort)
		}
		return append(v, VariantNone), nil
	}

	var out []string
	for _, v := range requested {
		v = strings.ToLower(strings.TrimSpace(v))
		switch v {
		case skillstore.VariantFull, VariantNone:
		case skillstore.VariantShort:
			if !hasShort {
				return nil, apperror.NewBadRequest("Validasi gagal", response.FieldError{Field: "variants", Message: "skill ini tidak punya SHORT.md"})
			}
		default:
			return nil, apperror.NewBadRequest("Validasi gagal", response.FieldError{Field: "variants", Message: "varian harus full, short, atau none"})
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out, nil
}

// skillVersion adalah hash pendek isi SKILL.md + SHORT.md.
func (s *Service) skillVersion(skill string) string {
	h := sha256.New()
	for _, f := range []string{skillFile, shortFile} {
		if b, err := s.Files.ReadFile(skill, f); err == nil {
			h.Write(b)
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// run memproses semua job dengan batas jumlah yang berjalan bersamaan, lalu
// menyimpan tiap hasil begitu selesai.
func (s *Service) run(runID string, jobs []job) {
	sem := make(chan struct{}, s.Concurrency)
	var wg sync.WaitGroup

	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()

			row := s.runJob(j)
			if err := s.Store.Save(&row); err != nil {
				logger.Error("simpan hasil eval gagal", zap.String("run_id", runID), zap.Error(err))
			}

			s.mu.Lock()
			if p := s.active[runID]; p != nil {
				p.done++
			}
			s.mu.Unlock()
		}(j)
	}
	wg.Wait()

	s.mu.Lock()
	delete(s.active, runID)
	s.mu.Unlock()
	logger.Info("eval selesai", zap.String("run_id", runID), zap.Int("jobs", len(jobs)))
}

// runJob menjawab satu pertanyaan dengan satu varian lalu menilainya.
func (s *Service) runJob(j job) EvalRun {
	row := EvalRun{
		RunID: j.runID, UserID: j.userID, Skill: j.skill, SkillVersion: j.version, Variant: j.variant,
		Model: j.model, JudgeModel: j.judgeModel,
		CaseIndex: j.index, Question: j.c.Question, Criteria: j.c.Criteria,
	}

	var messages []clients.ChatMessage
	if j.rendered != nil {
		messages = append(messages, clients.ChatMessage{Role: "system", Content: skillstore.Block(j.skill, j.rendered.Text)})
		row.SkillTokensEst = j.rendered.Tokens
	}
	messages = append(messages, clients.ChatMessage{Role: "user", Content: j.c.Question})

	// eval berjalan di latar belakang dengan akun 9Router milik pemanggil
	base := userctx.WithRouterKey(userctx.WithRouterURL(context.Background(), j.routerURL), j.routerKey)
	ctx, cancel := context.WithTimeout(base, callTimeout)
	defer cancel()

	start := time.Now()
	ans, err := s.LLM.Complete(ctx, j.model, messages)
	row.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		row.Error = "menjawab: " + err.Error()
		return row
	}

	row.Answer = ans.Text
	if ans.Usage != nil {
		pt, ct := ans.Usage.PromptTokens, ans.Usage.CompletionTokens
		row.PromptTokens, row.CompletionTokens = &pt, &ct
	}

	jctx, jcancel := context.WithTimeout(base, callTimeout)
	defer jcancel()

	score, reason, used, err := s.judge(jctx, j.judgeModel, j.c, ans.Text)
	row.JudgeTokens = used
	if err != nil {
		row.Error = "menilai: " + err.Error()
		return row
	}
	row.Score, row.Reason = &score, reason
	return row
}

// judge meminta model hakim menilai jawaban 0-10. Hakim tidak diberi tahu
// varian mana yang menjawab, supaya penilaiannya tidak berat sebelah.
func (s *Service) judge(ctx context.Context, judgeModel string, c Case, answer string) (float64, string, int, error) {
	prompt := "Pertanyaan:\n" + c.Question +
		"\n\nKriteria jawaban yang baik:\n" + c.Criteria +
		"\n\nJawaban yang dinilai:\n<<<\n" + answer + "\n>>>\n\n" +
		"Beri skor 0-10 seberapa baik jawaban memenuhi kriteria (10 = memenuhi semua kriteria dengan tepat, " +
		"0 = tidak menjawab atau salah). Balas HANYA dengan JSON: {\"score\": <angka 0-10>, \"reason\": \"<alasan singkat dalam bahasa Indonesia>\"}"

	messages := []clients.ChatMessage{
		{Role: "system", Content: "Kamu penilai jawaban yang ketat, adil, dan konsisten. Nilai hanya berdasarkan kriteria yang diberikan."},
		{Role: "user", Content: prompt},
	}

	out, err := s.LLM.Complete(ctx, judgeModel, messages)
	if err != nil {
		return 0, "", 0, err
	}

	used := tokens.Estimate(prompt) + tokens.Estimate(out.Text)
	if out.Usage != nil {
		used = out.Usage.PromptTokens + out.Usage.CompletionTokens
	}

	score, reason, err := parseVerdict(out.Text)
	return score, reason, used, err
}

// parseVerdict membaca JSON {"score","reason"} dari jawaban hakim, walau
// dibungkus teks lain atau blok kode.
func parseVerdict(text string) (float64, string, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return 0, "", fmt.Errorf("jawaban hakim bukan JSON: %q", clip(text, 120))
	}

	var v struct {
		Score  *float64 `json:"score"`
		Reason string   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &v); err != nil || v.Score == nil {
		return 0, "", fmt.Errorf("jawaban hakim tidak berisi skor: %q", clip(text, 120))
	}
	return math.Max(0, math.Min(10, *v.Score)), strings.TrimSpace(v.Reason), nil
}

// Summary meringkas satu eval per varian beserta kesimpulannya.
// userID "" = semua user (admin); selain itu hanya eval milik user itu.
func (s *Service) Summary(userID, runID string, detail bool) (*RunSummary, error) {
	rows, err := s.Store.ByRun(userID, runID)
	if err != nil {
		logger.Error("baca hasil eval gagal", zap.Error(err))
		return nil, apperror.NewInternalServerError("Gagal mengambil hasil eval")
	}

	s.mu.Lock()
	p, running := s.active[runID]
	var total, done int
	if running {
		total, done = p.total, p.done
	}
	s.mu.Unlock()

	if len(rows) == 0 && !running {
		return nil, apperror.NewNotFound("Eval tidak ditemukan")
	}

	sum := &RunSummary{RunID: runID, Status: "done", Total: len(rows), Completed: len(rows), Notes: []string{}}
	if running {
		sum.Status, sum.Total, sum.Completed = "running", total, done
	}
	if len(rows) > 0 {
		sum.Skill, sum.SkillVersion, sum.Model, sum.JudgeModel = rows[0].Skill, rows[0].SkillVersion, rows[0].Model, rows[0].JudgeModel
	}

	sum.Variants = summarize(rows)
	sum.Notes = conclude(sum.Variants)
	if running {
		sum.Notes = append(sum.Notes, "Eval masih berjalan; angka di atas belum final.")
	}
	if detail {
		sum.Rows = rows
	}
	return sum, nil
}

// Runs mengambil daftar eval terbaru.
func (s *Service) Runs(userID, skill string, limit int) ([]RunListItem, error) {
	rows, err := s.Store.Runs(userID, skill, limit)
	if err != nil {
		logger.Error("baca daftar eval gagal", zap.Error(err))
		return nil, apperror.NewInternalServerError("Gagal mengambil daftar eval")
	}
	return rows, nil
}

// summarize menghitung rata-rata per varian, dengan urutan full, short, none.
func summarize(rows []EvalRun) []VariantSummary {
	order := []string{skillstore.VariantFull, skillstore.VariantShort, VariantNone}
	by := map[string]*VariantSummary{}
	scoreSum, promptSum, promptN := map[string]float64{}, map[string]float64{}, map[string]int{}
	skillSum := map[string]float64{}

	for _, r := range rows {
		v := by[r.Variant]
		if v == nil {
			v = &VariantSummary{Variant: r.Variant}
			by[r.Variant] = v
			if !slices.Contains(order, r.Variant) {
				order = append(order, r.Variant)
			}
		}
		v.Cases++
		skillSum[r.Variant] += float64(r.SkillTokensEst)
		if r.Error != "" {
			v.Errors++
		}
		if r.Score != nil {
			v.Scored++
			scoreSum[r.Variant] += *r.Score
		}
		if r.PromptTokens != nil {
			promptSum[r.Variant] += float64(*r.PromptTokens)
			promptN[r.Variant]++
		}
	}

	out := []VariantSummary{}
	for _, name := range order {
		v := by[name]
		if v == nil {
			continue
		}
		v.AvgSkillTokens = round(skillSum[name] / float64(v.Cases))
		if v.Scored > 0 {
			avg := round(scoreSum[name] / float64(v.Scored))
			v.AvgScore = &avg
		}
		if promptN[name] > 0 {
			avg := round(promptSum[name] / float64(promptN[name]))
			v.AvgPromptTokens = &avg
		}
		out = append(out, *v)
	}
	return out
}

// conclude menarik kesimpulan sederhana dari ringkasan varian.
func conclude(vs []VariantSummary) []string {
	get := func(name string) *VariantSummary {
		for i := range vs {
			if vs[i].Variant == name && vs[i].AvgScore != nil {
				return &vs[i]
			}
		}
		return nil
	}
	full, short, none := get(skillstore.VariantFull), get(skillstore.VariantShort), get(VariantNone)

	var notes []string
	if full != nil && short != nil {
		diff := *short.AvgScore - *full.AvgScore
		saved := full.AvgSkillTokens - short.AvgSkillTokens
		hemat := ""
		if saved > 0 {
			hemat = fmt.Sprintf(" dan menghemat sekitar %.0f token per request", saved)
		}
		switch {
		case diff > equalTolerance:
			notes = append(notes, fmt.Sprintf("SHORT.md lebih baik %.2f poin dibanding SKILL.md%s: SHORT.md layak dipakai, dan periksa kenapa SKILL.md lebih buruk.", diff, hemat))
		case diff >= -equalTolerance && saved > 0:
			notes = append(notes, fmt.Sprintf("SHORT.md setara dengan SKILL.md (selisih skor %+.2f)%s: SHORT.md layak dipakai.", diff, hemat))
		case diff >= -equalTolerance:
			notes = append(notes, fmt.Sprintf("SHORT.md setara dengan SKILL.md (selisih skor %+.2f) tetapi tidak lebih pendek, jadi tidak ada penghematan token.", diff))
		default:
			notes = append(notes, fmt.Sprintf("SKILL.md lebih baik %.2f poin dibanding SHORT.md; SHORT.md perlu diperbaiki sebelum dipakai.", -diff))
		}
	}
	if full != nil && none != nil {
		gain := *full.AvgScore - *none.AvgScore
		switch {
		case gain >= equalTolerance:
			notes = append(notes, fmt.Sprintf("SKILL.md menaikkan skor %.2f poin dibanding tanpa skill.", gain))
		case gain > -equalTolerance:
			notes = append(notes, fmt.Sprintf("SKILL.md hampir tidak menaikkan skor dibanding tanpa skill (selisih %+.2f); pertimbangkan memperbaiki isi skill atau kriteria ujinya.", gain))
		default:
			notes = append(notes, fmt.Sprintf("SKILL.md justru menurunkan skor %.2f poin dibanding tanpa skill; lihat jawabannya dengan detail=true (mis. skill meminta data atau skrip yang tidak tersedia bagi model).", -gain))
		}
	}

	errs := 0
	for _, v := range vs {
		errs += v.Errors
	}
	if errs > 0 {
		notes = append(notes, fmt.Sprintf("%d jawaban gagal dijawab atau dinilai; lihat field error dengan detail=true.", errs))
	}
	if notes == nil {
		notes = []string{}
	}
	return notes
}

func round(f float64) float64 { return math.Round(f*100) / 100 }

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// newUUID membuat UUID v4 acak.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
