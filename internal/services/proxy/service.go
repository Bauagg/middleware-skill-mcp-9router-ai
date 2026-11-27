// Package proxy meneruskan request OpenAI-compatible ke 9Router. Untuk
// /v1/chat/completions, konteks tambahan disisipkan lebih dulu ke awal system
// prompt dengan urutan tetap: skill yang relevan, context pack, lalu catatan
// memori yang cocok dengan pesan user. Bila ada tool MCP yang cocok, tool itu
// ikut dikirim dan middleware menjalankan putaran panggil-tool (lihat RunWithTools).
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/attachment"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/llmtool"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/modelbudget"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/openai"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/tokens"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
	"github.com/Bauagg/middleware-skill-ai/internal/services/contextpack"
	"github.com/Bauagg/middleware-skill-ai/internal/services/memorynote"
	"github.com/Bauagg/middleware-skill-ai/internal/services/skillcache"
	"github.com/Bauagg/middleware-skill-ai/internal/services/skillindex"
	"github.com/Bauagg/middleware-skill-ai/internal/services/skillstore"
	"github.com/Bauagg/middleware-skill-ai/internal/services/usagelog"
)

const (
	// chatPath dicatat di log pemakaian.
	chatPath = "/v1/chat/completions"

	// recentTurnLimit: pesan sebelumnya yang ikut dikirim sebagai konteks
	// ekstraksi memori.
	recentTurnLimit = 6
)

// Selector memilih skill untuk sebuah pesan; skillindex.Service memenuhinya.
// Didefinisikan di sini sebagai kebutuhan Service.
type Selector interface {
	Select(ctx context.Context, query string) skillindex.Selection
	Choose(sel skillindex.Selection, activeDir string) (*skillindex.Match, skillindex.Decision)
	Version() string
	Lookup(dir string) (skillindex.IndexEntry, bool)
}

// Renderer menyiapkan teks skill sesuai budget token; skillstore.Service memenuhinya.
type Renderer interface {
	Render(dir string, budget int) (*skillstore.Rendered, error)
}

// Cache menyimpan hasil seleksi dan skill aktif percakapan; skillcache.Service memenuhinya.
type Cache interface {
	GetSelection(ctx context.Context, version, query string) (skillcache.Selection, skillcache.State)
	SetSelection(ctx context.Context, version, query string, sel skillcache.Selection)
	GetSession(ctx context.Context, conversationKey string) (string, skillcache.State)
	SetSession(ctx context.Context, conversationKey, dir string)
}

// Upstream adalah tujuan request (9Router); clients.NineRouter memenuhinya.
// ChatStream dipakai putaran tool (streaming, dirakit middleware).
type Upstream interface {
	Forward(w http.ResponseWriter, r *http.Request) clients.Result
	ChatStream(ctx context.Context, body []byte) (*http.Response, error)
}

// Tools adalah sumber tool untuk model: memilih tool yang cocok dengan pesan,
// mengenali tool miliknya, dan menjalankannya. mcp.Service dan
// imagegen.Service memenuhinya; beberapa sumber digabung dengan ToolSources.
type Tools interface {
	Select(ctx context.Context, query string) []llmtool.Spec
	Has(ctx context.Context, name string) bool
	Call(ctx context.Context, name, args string) llmtool.Result
}

// ToolSources menggabungkan beberapa sumber tool menjadi satu.
type ToolSources []Tools

// Select mengumpulkan tool yang cocok dari semua sumber.
func (ts ToolSources) Select(ctx context.Context, query string) []llmtool.Spec {
	var out []llmtool.Spec
	for _, t := range ts {
		out = append(out, t.Select(ctx, query)...)
	}
	return out
}

// Has melaporkan apakah salah satu sumber memiliki tool ini.
func (ts ToolSources) Has(ctx context.Context, name string) bool {
	for _, t := range ts {
		if t.Has(ctx, name) {
			return true
		}
	}
	return false
}

// Call menjalankan tool di sumber pemiliknya.
func (ts ToolSources) Call(ctx context.Context, name, args string) llmtool.Result {
	for _, t := range ts {
		if t.Has(ctx, name) {
			return t.Call(ctx, name, args)
		}
	}
	return llmtool.Result{Text: "tool " + name + " tidak dikenal", IsError: true}
}

// Recorder mencatat pemakaian satu request; usagelog.Service memenuhinya.
// Record tidak boleh menunda atau menggagalkan request.
type Recorder interface {
	Record(e usagelog.Entry)
}

// ContextPacks memberi teks context pack aktif; contextpack.Service memenuhinya.
// ActiveText tidak boleh menggagalkan request ("" bila tidak ada/gagal).
type ContextPacks interface {
	ActiveText(ctx context.Context) string
}

// Memory mencari catatan memori yang relevan dan mengamati pesan untuk
// diekstrak; memorynote.Service memenuhinya. Keduanya fail-open.
type Memory interface {
	Relevant(ctx context.Context, query string) []memorynote.Hit
	Block(hits []memorynote.Hit) string
	Observe(ctx context.Context, userID, userText string, recent []memorynote.Turn)
}

// Fallback menjawab chat dengan akun AI milik user (Gemini, API kompatibel
// OpenAI, ...) bila 9Router-nya tidak bisa dipakai (kuota habis, error,
// tidak terjangkau); aiaccount.Service memenuhinya.
type Fallback interface {
	CanChat(ctx context.Context) bool
	Chat(ctx context.Context, messages []clients.ChatMessage) (text, model string, err error)
}

// Deps adalah semua yang dibutuhkan Service.
type Deps struct {
	Selector     Selector
	Renderer     Renderer
	Cache        Cache
	Upstream     Upstream
	Recorder     Recorder     // boleh nil (pencatatan dimatikan)
	ContextPacks ContextPacks // boleh nil (context pack dimatikan)
	Memory       Memory       // boleh nil (catatan memori dimatikan)
	Tools        Tools        // boleh nil (tidak ada tool: MCP dan pembuat gambar mati)
	Fallback     Fallback     // boleh nil (tanpa cadangan saat 9Router gagal)
	ToolMaxSteps int          // putaran panggil-tool maksimal per request
	Budgets      modelbudget.Budgets
}

// Service berisi logika penyisipan skill, penerusan ke 9Router, dan pencatatan pemakaian.
type Service struct {
	Deps
}

// NewService membuat Service.
func NewService(deps Deps) *Service {
	return &Service{Deps: deps}
}

// Forward meneruskan request ke 9Router, mengalirkan responsnya ke w, lalu
// mengembalikan hasil pengamatan (status, token, dan apakah client memutus koneksi).
func (s *Service) Forward(w http.ResponseWriter, r *http.Request) clients.Result {
	return s.Upstream.Forward(w, r)
}

// ForwardChat meneruskan chat tanpa tool ke 9Router seperti Forward. Bila
// 9Router gagal karena masalah di sisinya (kuota habis, error, tidak
// terjangkau) dan user punya akun AI yang bisa menjawab, respons gagal itu
// ditahan dan jawabannya diambil dari akun AI user.
func (s *Service) ForwardChat(w http.ResponseWriter, r *http.Request, p *Prepared) clients.Result {
	if s.Fallback == nil {
		return s.Upstream.Forward(w, r)
	}
	ctx := r.Context()
	res := s.Upstream.Forward(w, r.WithContext(clients.WithFallback(ctx, func(status int) bool {
		return s.canFallback(ctx, status)
	})))
	if !res.Fallback {
		return res
	}

	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(p.Body, &body)
	out := newToolWriter(w, p.Stream, p.Model)
	defer out.stop()
	text, model, err := s.fallbackChat(ctx, out, body.Messages, res.Status, res.UpstreamError)
	if err != nil {
		if ctx.Err() != nil {
			return clients.Result{Aborted: true}
		}
		out.fail(res.Status, err.Error(), "upstream_error")
		return clients.Result{Status: res.Status}
	}
	out.text(text)
	out.finish(model, text, "stop", nil, false)
	return clients.Result{Status: http.StatusOK}
}

// canFallback melaporkan apakah kegagalan 9Router dengan status ini dijawab
// akun AI user: masalah di sisi 9Router/provider-nya (key/kuota ditolak,
// limit, error server, tidak terjangkau), bukan request yang salah (400).
func (s *Service) canFallback(ctx context.Context, status int) bool {
	if s.Fallback == nil {
		return false
	}
	switch {
	case status == http.StatusUnauthorized, status == http.StatusPaymentRequired, status == http.StatusForbidden,
		status == http.StatusTooManyRequests, status >= 500:
		return s.Fallback.CanChat(ctx)
	}
	return false
}

// fallbackChat menjawab percakapan dengan akun AI user setelah 9Router gagal.
// Bila akun AI juga gagal, error-nya memuat kedua alasan.
func (s *Service) fallbackChat(ctx context.Context, out *toolWriter, messages []json.RawMessage, status int, upstream string) (string, string, error) {
	logger.Warn("9Router gagal, chat dijawab akun AI user", zap.Int("status", status), zap.String("error", clipText(upstream, 300)))
	out.comment("9Router gagal; menjawab dengan akun AI")
	text, model, err := s.Fallback.Chat(ctx, chatMessages(messages))
	if err != nil {
		logger.Warn("akun AI juga gagal menjawab chat", zap.Error(err))
		return "", "", fmt.Errorf("9Router gagal (%d): %s; akun AI juga gagal: %w", status, upstream, err)
	}
	return text, model, nil
}

// chatMessages mengubah pesan berformat OpenAI menjadi teks per peran.
// Permintaan tool dari model ditulis sebagai teks, karena akun AI cadangan
// tidak menjalankan tool.
func chatMessages(raw []json.RawMessage) []clients.ChatMessage {
	out := make([]clients.ChatMessage, 0, len(raw))
	for _, r := range raw {
		var m struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []toolCall      `json:"tool_calls"`
		}
		if json.Unmarshal(r, &m) != nil {
			continue
		}
		text := contentText(m.Content)
		if text == "" && len(m.ToolCalls) > 0 {
			names := make([]string, len(m.ToolCalls))
			for i, c := range m.ToolCalls {
				names[i] = c.Function.Name
			}
			text = "(memanggil tool: " + strings.Join(names, ", ") + ")"
		}
		if text != "" {
			out = append(out, clients.ChatMessage{Role: m.Role, Content: text})
		}
	}
	return out
}

// RecordUsage mencatat satu request chat completions yang sudah selesai.
func (s *Service) RecordUsage(p *Prepared, res clients.Result, elapsed time.Duration) {
	if s.Recorder == nil {
		return
	}

	e := usagelog.Entry{
		UserID:          p.UserID,
		Model:           p.Model,
		Path:            chatPath,
		Stream:          p.Stream,
		ExtraTokensEst:  p.ExtraTokensEst,
		BudgetTokens:    p.BudgetTokens,
		SelectionCache:  p.SelectionCache,
		SessionCache:    p.SessionCache,
		SelectionMethod: p.SelectionMethod,
		FallbackTokens:  p.FallbackTokens,

		ContextPackTokensEst: p.ContextPackTokensEst,
		MemoryNotes:          len(p.MemoryNoteIDs),
		MemoryNoteIDs:        strings.Join(p.MemoryNoteIDs, ","),
		MemoryTokensEst:      p.MemoryTokensEst,

		MCPToolsOffered:   len(p.ToolNames),
		MCPToolsTokensEst: p.ToolsTokensEst,
		MCPToolCalls:      len(p.ToolCalls),
		MCPToolNames:      strings.Join(p.ToolCalls, ","),
		MCPSteps:          p.ToolSteps,

		Status:    res.Status,
		LatencyMs: elapsed.Milliseconds(),
		Aborted:   res.Aborted,
	}
	if p.Skill != "" {
		e.SkillName, e.SkillScore, e.SkillVariant = &p.Skill, &p.Score, &p.SkillVariant
	}
	if u := res.Usage; u != nil {
		e.PromptTokens, e.CompletionTokens = &u.PromptTokens, &u.CompletionTokens
		if u.CachedTokens > 0 {
			e.CachedTokens = &u.CachedTokens
		}
	}

	s.Recorder.Record(e)
}

// Learn dipanggil setelah respons selesai: pesan user diserahkan ke memori
// untuk diekstrak menjadi catatan usulan bila memuat sinyal hasil. Berjalan di
// latar belakang, tidak menunda respons.
func (s *Service) Learn(ctx context.Context, p *Prepared) {
	if s.Memory == nil || p.Query == "" {
		return
	}
	s.Memory.Observe(ctx, p.UserID, p.Query, p.Recent)
}

// Prepare menyisipkan konteks tambahan ke awal system prompt pada body
// /v1/chat/completions. Urutannya:
//  1. kandidat skill dari seleksi BM25 (dengan cache hasil seleksi);
//  2. ditimbang terhadap skill aktif percakapan (cache sesi);
//  3. teks skill disiapkan sesuai budget token model (dibersihkan, SHORT.md,
//     atau dipotong);
//  4. context pack aktif dan catatan memori yang cocok ditambahkan setelah
//     skill (juga saat tidak ada skill terpilih), lalu semuanya disisipkan
//     sekaligus sebagai satu blok.
//
// Prepared selalu non-nil. Bila tidak ada yang disisipkan, Body sama
// dengan raw. Bila terjadi error, Prepared tetap berisi raw: pemanggil cukup
// mencatat error-nya dan meneruskan request tanpa skill (fail-open).
//
// Hanya field "messages" yang disentuh; field lain dan field tak dikenal di
// dalam body maupun di dalam tiap pesan dibiarkan apa adanya.
func (s *Service) Prepare(ctx context.Context, raw []byte, meta RequestMeta) (*Prepared, error) {
	p := &Prepared{Body: raw, UserID: meta.UserID}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return p, fmt.Errorf("body bukan JSON: %w", err)
	}
	_ = json.Unmarshal(body["model"], &p.Model)
	_ = json.Unmarshal(body["stream"], &p.Stream)

	// model wajib dari client: setiap user memakai model 9Router miliknya sendiri
	if strings.TrimSpace(p.Model) == "" {
		return p, ErrModelRequired
	}
	// pemilih cadangan dan tool memakai model yang sama dengan chat user
	ctx = userctx.WithRouterModel(ctx, p.Model)

	changed := false
	p.BudgetTokens = s.Budgets.For(p.Model)

	// Standar OpenAI: stream bawaannya false. 9Router justru menjawab SSE bila
	// field ini tidak ada, sehingga SDK OpenAI yang tidak mengirimnya menerima
	// SSE padahal menunggu JSON. Jadi nilainya ditulis eksplisit.
	if _, ok := body["stream"]; !ok {
		body["stream"] = json.RawMessage("false")
		changed = true
	}
	if changed {
		if out, err := json.Marshal(body); err == nil {
			p.Body = out
		}
	}

	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(body["messages"], &messages); err != nil {
		return p, fmt.Errorf("field messages tidak valid: %w", err)
	}

	query := lastUserText(messages)
	p.Query = query
	p.Recent = recentTurns(messages, recentTurnLimit)
	convKey := skillcache.ConversationKey(meta.UserID, meta.ConversationID, firstUserText(messages))

	// 1. hasil seleksi (BM25/embedding, pemilih cadangan bila ambigu, atau dari cache)
	sel, selState := s.selectSkill(ctx, query)
	if query != "" {
		p.SelectionCache = string(selState)
	}
	p.SelectionMethod = sel.Method
	p.FallbackTokens = sel.FallbackTokens

	// 2. timbang terhadap skill aktif percakapan
	activeDir, sessState := s.Cache.GetSession(ctx, convKey)
	chosen, decision := s.Selector.Choose(sel, activeDir)
	p.SessionCache = sessionLabel(sessState, decision)

	// 3-4. susun blok: skill -> context pack -> catatan memori
	var blocks []string

	var rendered *skillstore.Rendered
	if chosen != nil {
		var err error
		if rendered, err = s.Renderer.Render(chosen.Entry.Dir, p.BudgetTokens); err != nil {
			// skill gagal dibaca: tetap lanjut dengan context pack dan memori
			logger.Warn("skill gagal disiapkan, dilewati", zap.String("skill", chosen.Entry.Name), zap.Error(err))
			chosen = nil
		} else {
			block := skillstore.Block(chosen.Entry.Name, rendered.Text)
			blocks = append(blocks, block)
			p.ExtraTokensEst = tokens.Estimate(block)
		}
	}

	if s.ContextPacks != nil {
		if text := s.ContextPacks.ActiveText(ctx); text != "" {
			block := contextpack.Block(text)
			blocks = append(blocks, block)
			p.ContextPackTokensEst = tokens.Estimate(block)
		}
	}

	if s.Memory != nil && query != "" {
		if hits := s.Memory.Relevant(ctx, query); len(hits) > 0 {
			block := s.Memory.Block(hits)
			blocks = append(blocks, block)
			p.MemoryTokensEst = tokens.Estimate(block)
			for _, h := range hits {
				p.MemoryNoteIDs = append(p.MemoryNoteIDs, h.ID)
			}
		}
	}

	// 5. tool MCP yang cocok dengan pesan (bukan bila client membawa tool sendiri,
	// karena tool client harus dijalankan client, bukan middleware)
	if s.Tools != nil && query != "" && !hasClientTools(body) {
		if specs := s.Tools.Select(ctx, toolQuery(messages)); len(specs) > 0 {
			defs := make([]json.RawMessage, 0, len(specs))
			for _, t := range specs {
				defs = append(defs, t.Definition)
				p.ToolNames = append(p.ToolNames, t.Name)
			}
			raw, _ := json.Marshal(defs)
			body["tools"] = raw
			p.ToolsTokensEst = tokens.Estimate(string(raw))
		}
	}

	if len(blocks) == 0 && len(p.ToolNames) == 0 {
		return p, nil
	}

	var err error
	if len(blocks) > 0 {
		if messages, err = injectSystem(messages, strings.Join(blocks, "\n\n")); err != nil {
			p.resetInjection()
			return p, err
		}
		if body["messages"], err = json.Marshal(messages); err != nil {
			p.resetInjection()
			return p, fmt.Errorf("susun messages: %w", err)
		}
	}
	out, err := json.Marshal(body)
	if err != nil {
		p.resetInjection()
		return p, fmt.Errorf("susun body: %w", err)
	}
	p.Body = out

	// skill aktif disimpan (atau diperpanjang) hanya setelah berhasil disisipkan
	if chosen != nil {
		s.Cache.SetSession(ctx, convKey, chosen.Entry.Dir)
		p.Skill = chosen.Entry.Name
		p.Score = chosen.Score
		p.SkillVariant = rendered.Variant
	}
	return p, nil
}

// selectSkill menjalankan seleksi lewat cache hasil seleksi: bila pesan
// serupa pernah dihitung pada versi index yang sama, hasilnya (termasuk
// keputusan pemilih cadangan) dipakai ulang tanpa menghitung atau memanggil
// model lagi. Hasil yang "degraded" (embedding/pemilih cadangan gagal) tidak
// disimpan, supaya kegagalan sesaat tidak terbawa selama masa cache.
func (s *Service) selectSkill(ctx context.Context, query string) (skillindex.Selection, skillcache.State) {
	if query == "" {
		return skillindex.Selection{}, skillcache.Off
	}

	version := s.Selector.Version()

	cached, state := s.Cache.GetSelection(ctx, version, query)
	if state == skillcache.Hit {
		if sel, ok := s.fromCache(cached); ok {
			return sel, state
		}
		state = skillcache.Miss // skill di cache sudah tidak ada di index
	}

	sel := s.Selector.Select(ctx, query)
	if state != skillcache.Off && !sel.Degraded {
		s.Cache.SetSelection(ctx, version, query, toCache(sel))
	}
	return sel, state
}

// toCache mengubah hasil seleksi ke bentuk yang disimpan di cache.
func toCache(sel skillindex.Selection) skillcache.Selection {
	c := skillcache.Selection{Method: sel.Method}
	if sel.Best != nil {
		c.Dir, c.Score = sel.Best.Entry.Dir, sel.Best.Score
	}
	for _, m := range sel.Close {
		c.Close = append(c.Close, skillcache.Scored{Dir: m.Entry.Dir, Score: m.Score})
	}
	return c
}

// fromCache mengubah isi cache kembali menjadi hasil seleksi. false bila
// skill terpilih sudah tidak ada di index.
func (s *Service) fromCache(c skillcache.Selection) (skillindex.Selection, bool) {
	sel := skillindex.Selection{Method: c.Method}

	if c.Dir != "" {
		entry, ok := s.Selector.Lookup(c.Dir)
		if !ok {
			return sel, false
		}
		sel.Best = &skillindex.Match{Entry: entry, Score: c.Score}
	}

	for _, sc := range c.Close {
		if entry, ok := s.Selector.Lookup(sc.Dir); ok {
			sel.Close = append(sel.Close, skillindex.Match{Entry: entry, Score: sc.Score})
		}
	}
	return sel, true
}

// sessionLabel merangkum status cache sesi untuk log: "hit" (skill aktif
// ada dan dipakai), "switch" (ada tapi diganti), "miss", atau "off".
func sessionLabel(state skillcache.State, decision skillindex.Decision) string {
	if state == skillcache.Hit && decision == skillindex.DecisionSwitch {
		return "switch"
	}
	return string(state)
}

// roleOf membaca field role sebuah pesan ("" bila tidak ada).
func roleOf(m map[string]json.RawMessage) string {
	var role string
	_ = json.Unmarshal(m["role"], &role)
	return role
}

// lastUserText mengambil teks dari pesan user terakhir.
func lastUserText(messages []map[string]json.RawMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if roleOf(messages[i]) == "user" {
			return strings.TrimSpace(typedText(messages[i]["content"]))
		}
	}
	return ""
}

// recentTurns mengambil sampai n pesan user/assistant sebelum pesan user
// terakhir (teks saja), sebagai konteks ekstraksi memori.
func recentTurns(messages []map[string]json.RawMessage, n int) []memorynote.Turn {
	last := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if roleOf(messages[i]) == "user" {
			last = i
			break
		}
	}

	var turns []memorynote.Turn
	for i := last - 1; i >= 0 && len(turns) < n; i-- {
		role := roleOf(messages[i])
		if role != "user" && role != "assistant" {
			continue
		}
		if text := strings.TrimSpace(typedText(messages[i]["content"])); text != "" {
			turns = append(turns, memorynote.Turn{Role: role, Text: text})
		}
	}

	// dibalik supaya urut dari yang paling lama
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	return turns
}

// toolQuery adalah teks untuk memilih tool: dua pesan user terakhir, supaya
// pesan lanjutan seperti "ubah warnanya jadi biru" tetap membawa tool dari
// topik sebelumnya ("ambil frame login di figma").
func toolQuery(messages []map[string]json.RawMessage) string {
	var parts []string
	for i := len(messages) - 1; i >= 0 && len(parts) < 2; i-- {
		if roleOf(messages[i]) == "user" {
			if t := strings.TrimSpace(typedText(messages[i]["content"])); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// clipText memotong s menjadi paling banyak n karakter.
func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// hasClientTools melaporkan apakah client sudah membawa tool sendiri.
func hasClientTools(body map[string]json.RawMessage) bool {
	for _, k := range []string{"tools", "functions"} {
		var arr []json.RawMessage
		if json.Unmarshal(body[k], &arr) == nil && len(arr) > 0 {
			return true
		}
	}
	return false
}

// firstUserText mengambil teks dari pesan user pertama (dasar pengenal percakapan).
func firstUserText(messages []map[string]json.RawMessage) string {
	for _, m := range messages {
		if roleOf(m) == "user" {
			return strings.TrimSpace(typedText(m["content"]))
		}
	}
	return ""
}

// typedText seperti contentText tetapi tanpa isi file lampiran (lihat
// attachment.Expand): skill, tool, dan memori dipilih dari teks yang diketik
// user, bukan dari isi dokumen yang dilampirkan.
func typedText(raw json.RawMessage) string {
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return contentText(raw)
	}
	var texts []string
	for _, p := range parts {
		if (p.Type == "text" || p.Type == "") && p.Text != "" && !strings.HasPrefix(p.Text, attachment.Marker) {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// contentText membaca content yang bisa berupa string atau array bagian
// ([{type:"text", text:"..."}]); bagian non-teks seperti gambar diabaikan.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}

	var texts []string
	for _, p := range parts {
		if (p.Type == "text" || p.Type == "") && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// injectSystem menaruh block di awal pesan system yang sudah ada. Bila tidak
// ada pesan system (atau developer), dibuat satu di paling depan.
func injectSystem(messages []map[string]json.RawMessage, block string) ([]map[string]json.RawMessage, error) {
	for _, m := range messages {
		if role := roleOf(m); role != "system" && role != "developer" {
			continue
		}

		content, err := prependToContent(m["content"], block)
		if err != nil {
			return nil, err
		}
		m["content"] = content
		return messages, nil
	}

	role, _ := json.Marshal("system")
	content, _ := json.Marshal(block)
	system := map[string]json.RawMessage{"role": role, "content": content}
	return append([]map[string]json.RawMessage{system}, messages...), nil
}

// prependToContent menaruh block di depan content yang bisa berupa string,
// array bagian, atau kosong.
func prependToContent(raw json.RawMessage, block string) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return json.Marshal(block)
	}

	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return json.Marshal(block)
		}
		return json.Marshal(block + "\n\n" + s)
	}

	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) == nil {
		first, _ := json.Marshal(map[string]string{"type": "text", "text": block})
		return json.Marshal(append([]json.RawMessage{first}, parts...))
	}

	return nil, errors.New("format content pesan system tidak dikenal")
}

// ---------------------------------------------------------------------------
// putaran tool (MCP, gambar, web search, skill)
// ---------------------------------------------------------------------------

// keepaliveInterval: selama tool berjalan, client streaming dikirimi komentar
// SSE supaya koneksinya tidak dianggap mati oleh client atau proxy di depan.
const keepaliveInterval = 10 * time.Second

// maxSSELine membatasi satu baris SSE dari 9Router.
const maxSSELine = 8 << 20

// toolCall adalah satu permintaan tool dari model.
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// stepResult adalah jawaban satu putaran model, dirakit dari potongan SSE.
type stepResult struct {
	model  string
	finish string
	text   strings.Builder
	calls  []*toolCall
	usage  *openai.Usage
}

// RunWithTools menjalankan request yang membawa tool:
//
//  1. kirim ke 9Router dengan streaming (supaya koneksi tidak pernah diam:
//     Cloudflare di depan 9Router memutus koneksi yang diam sekitar 100
//     detik, dan menulis jawaban panjang seperti satu skill bisa lebih lama);
//  2. teks jawaban langsung diteruskan ke client streaming per potongan;
//  3. bila model meminta tool, semua tool dijalankan (bersamaan), hasilnya
//     ditambahkan ke percakapan, lalu dikirim lagi;
//  4. diulang sampai model menjawab tanpa meminta tool, atau batas
//     ToolMaxSteps tercapai (putaran terakhir memakai tool_choice "none");
//  5. client non-stream menerima JSON utuh di akhir.
//
// Pemakaian token semua putaran dijumlahkan.
func (s *Service) RunWithTools(w http.ResponseWriter, r *http.Request, p *Prepared) clients.Result {
	ctx := r.Context()
	res := clients.Result{}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(p.Body, &body); err != nil {
		return s.Upstream.Forward(w, r) // tidak seharusnya terjadi: body sudah disusun Prepare
	}
	var messages []json.RawMessage
	_ = json.Unmarshal(body["messages"], &messages)

	includeUsage := streamIncludeUsage(body["stream_options"])
	body["stream"] = json.RawMessage("true")
	body["stream_options"] = json.RawMessage(`{"include_usage":true}`)

	out := newToolWriter(w, p.Stream, p.Model)
	defer out.stop()

	maxSteps := max(s.ToolMaxSteps, 1)
	var total openai.Usage
	hasUsage := false
	var allText strings.Builder

	for step := 1; ; step++ {
		last := step > maxSteps
		if last {
			body["tool_choice"] = json.RawMessage(`"none"`)
		}
		body["messages"], _ = json.Marshal(messages)
		reqBody, _ := json.Marshal(body)

		needSep := allText.Len() > 0
		onText := func(t string) {
			if needSep {
				allText.WriteString("\n\n")
				out.text("\n\n")
				needSep = false
			}
			allText.WriteString(t)
			out.text(t)
		}

		st, status, errBody, err := s.chatStep(ctx, reqBody, onText)
		p.ToolSteps = step
		if err != nil && ctx.Err() == nil {
			status = http.StatusBadGateway
		}
		if (err != nil || status/100 != 2) && ctx.Err() == nil && s.canFallback(ctx, status) {
			upstream := ""
			if err != nil {
				upstream = err.Error()
			} else {
				upstream = errorMessage(errBody)
			}
			text, model, ferr := s.fallbackChat(ctx, out, messages, status, upstream)
			if ferr == nil {
				onText(text)
				res.Status = http.StatusOK
				out.finish(model, allText.String(), "stop", res.Usage, includeUsage)
				return res
			}
			if ctx.Err() != nil {
				res.Aborted = true
				return res
			}
			res.Status = status
			out.fail(status, ferr.Error(), "upstream_error")
			return res
		}
		if err != nil {
			if ctx.Err() != nil {
				res.Aborted = true
				return res
			}
			logger.Error("putaran tool: 9Router gagal", zap.Error(err))
			res.Status = http.StatusBadGateway
			out.fail(res.Status, "9Router tidak dapat dihubungi: "+err.Error(), "bad_gateway")
			return res
		}
		if status/100 != 2 {
			res.Status = status
			out.passthrough(status, errBody)
			return res
		}

		if u := st.usage; u != nil {
			hasUsage = true
			total.PromptTokens += u.PromptTokens
			total.CompletionTokens += u.CompletionTokens
			total.TotalTokens += u.TotalTokens
			total.CachedTokens += u.CachedTokens
		}
		if hasUsage {
			u := total
			res.Usage = &u
		}

		if len(st.calls) == 0 || last {
			res.Status = http.StatusOK
			out.finish(st.model, allText.String(), st.finish, res.Usage, includeUsage)
			return res
		}

		// jalankan tool yang diminta, lalu lanjutkan percakapan dengan hasilnya
		msg := map[string]any{"role": "assistant", "content": nil, "tool_calls": st.calls}
		if t := st.text.String(); t != "" {
			msg["content"] = t
		}
		rawMsg, _ := json.Marshal(msg)
		messages = append(messages, rawMsg)

		names := make([]string, len(st.calls))
		for i, c := range st.calls {
			names[i] = c.Function.Name
		}
		out.comment("menjalankan tool: " + strings.Join(names, ", "))

		results := make([]llmtool.Result, len(st.calls))
		var wg sync.WaitGroup
		for i, c := range st.calls {
			wg.Add(1)
			go func(i int, c *toolCall) {
				defer wg.Done()
				results[i] = s.Tools.Call(ctx, c.Function.Name, c.Function.Arguments)
			}(i, c)
		}
		wg.Wait()

		for i, c := range st.calls {
			text := results[i].Text
			if results[i].IsError {
				// tanpa argumen: bisa memuat data rahasia milik tool MCP lain
				logger.Warn("tool chat gagal", zap.String("tool", c.Function.Name), zap.Int("step", step),
					zap.String("error", clipText(text, 300)))
				text = "ERROR: " + text
			}
			m, _ := json.Marshal(map[string]string{"role": "tool", "tool_call_id": c.ID, "content": text})
			messages = append(messages, m)
			p.ToolCalls = append(p.ToolCalls, c.Function.Name)
		}
	}
}

// chatStep mengirim satu putaran ke 9Router dengan streaming dan merakit
// potongan SSE menjadi teks, tool_calls, finish_reason, dan usage. onText
// dipanggil untuk setiap potongan teks. Bila 9Router menolak, status dan body
// error dikembalikan (err nil).
func (s *Service) chatStep(ctx context.Context, body []byte, onText func(string)) (*stepResult, int, []byte, error) {
	resp, err := s.Upstream.ChatStream(ctx, body)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, resp.StatusCode, raw, nil
	}

	st := &stepResult{}
	if !strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		// 9Router mengabaikan stream: jawaban utuh berbentuk JSON
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSSELine))
		if err != nil {
			return nil, 0, nil, err
		}
		return fromJSON(raw, onText)
	}

	sniff := openai.NewUsageSniffer("text/event-stream")
	byIndex := map[int]*toolCall{}
	var order []int

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxSSELine)
	for sc.Scan() {
		line := sc.Bytes()
		_, _ = sniff.Write(append(append([]byte{}, line...), '\n'))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[5:])
		if string(data) == "[DONE]" {
			break
		}

		var ch struct {
			Model   string          `json:"model"`
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Delta struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &ch) != nil {
			continue
		}
		if len(ch.Error) > 0 && string(ch.Error) != "null" {
			return nil, http.StatusBadGateway, append([]byte(`{"error":`), append(ch.Error, '}')...), nil
		}
		if ch.Model != "" {
			st.model = ch.Model
		}
		for _, c := range ch.Choices {
			if c.Delta.Content != nil && *c.Delta.Content != "" {
				st.text.WriteString(*c.Delta.Content)
				onText(*c.Delta.Content)
			}
			for _, tc := range c.Delta.ToolCalls {
				call := byIndex[tc.Index]
				if call == nil {
					call = &toolCall{Type: "function"}
					byIndex[tc.Index] = call
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					call.ID = tc.ID
				}
				if tc.Function.Name != "" {
					call.Function.Name += tc.Function.Name
				}
				call.Function.Arguments += tc.Function.Arguments
			}
			if c.FinishReason != nil && *c.FinishReason != "" {
				st.finish = *c.FinishReason
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, nil, fmt.Errorf("baca stream 9Router: %w", err)
	}

	for i, idx := range order {
		c := byIndex[idx]
		if c.Function.Name == "" {
			continue
		}
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		st.calls = append(st.calls, c)
	}
	st.usage = sniff.Usage()
	return st, http.StatusOK, nil, nil
}

// errorMessage mengambil pesan dari body error berformat OpenAI.
func errorMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return clipText(strings.TrimSpace(string(raw)), 300)
}

// fromJSON merakit stepResult dari jawaban non-stream (bila 9Router
// mengabaikan permintaan streaming).
func fromJSON(raw []byte, onText func(string)) (*stepResult, int, []byte, error) {
	var r struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   json.RawMessage `json:"content"`
				ToolCalls []*toolCall     `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || len(r.Choices) == 0 {
		return nil, http.StatusBadGateway, raw, nil
	}
	st := &stepResult{model: r.Model, finish: r.Choices[0].FinishReason}
	if t := contentText(r.Choices[0].Message.Content); t != "" {
		st.text.WriteString(t)
		onText(t)
	}
	for _, c := range r.Choices[0].Message.ToolCalls {
		if c != nil && c.Function.Name != "" {
			st.calls = append(st.calls, c)
		}
	}
	sn := openai.NewUsageSniffer("application/json")
	_, _ = sn.Write(raw)
	st.usage = sn.Usage()
	return st, http.StatusOK, nil, nil
}

// streamIncludeUsage membaca stream_options.include_usage dari request.
func streamIncludeUsage(raw json.RawMessage) bool {
	var o struct {
		IncludeUsage bool `json:"include_usage"`
	}
	_ = json.Unmarshal(raw, &o)
	return o.IncludeUsage
}

// toolWriter menulis hasil putaran tool ke client: SSE per potongan (bila
// client meminta stream) atau JSON utuh di akhir.
type toolWriter struct {
	w       http.ResponseWriter
	stream  bool
	id      string
	model   string
	created int64

	mu      sync.Mutex
	started bool // potongan pertama (berisi role) sudah dikirim
	done    chan struct{}
}

func newToolWriter(w http.ResponseWriter, stream bool, model string) *toolWriter {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	t := &toolWriter{w: w, stream: stream, id: "chatcmpl-mw-" + hex.EncodeToString(b), model: model,
		created: time.Now().Unix(), done: make(chan struct{})}
	if !stream {
		return t
	}

	// header SSE dikirim di awal supaya keepalive bisa langsung jalan
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // NGINX: jangan tampung respons
	w.WriteHeader(http.StatusOK)
	t.flush()

	go func() {
		tick := time.NewTicker(keepaliveInterval)
		defer tick.Stop()
		for {
			select {
			case <-t.done:
				return
			case <-tick.C:
				t.comment("keepalive")
			}
		}
	}()
	return t
}

func (t *toolWriter) stop() { close(t.done) }

func (t *toolWriter) flush() {
	if f, ok := t.w.(http.Flusher); ok {
		f.Flush()
	}
}

// comment mengirim komentar SSE (diabaikan client), hanya untuk stream.
func (t *toolWriter) comment(text string) {
	if !t.stream {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = io.WriteString(t.w, ": "+strings.ReplaceAll(text, "\n", " ")+"\n\n")
	t.flush()
}

func (t *toolWriter) event(v any) {
	b, _ := json.Marshal(v)
	_, _ = io.WriteString(t.w, "data: "+string(b)+"\n\n")
}

func (t *toolWriter) chunk(choices any) map[string]any {
	return map[string]any{"id": t.id, "object": "chat.completion.chunk", "created": t.created, "model": t.model, "choices": choices}
}

// text meneruskan satu potongan teks ke client streaming.
func (t *toolWriter) text(s string) {
	if !t.stream || s == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delta := map[string]any{"content": s}
	if !t.started {
		delta["role"] = "assistant"
		t.started = true
	}
	t.event(t.chunk([]any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}))
	t.flush()
}

// fail mengirim error berformat OpenAI.
func (t *toolWriter) fail(status int, msg, typ string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stream {
		t.event(map[string]any{"error": map[string]string{"message": msg, "type": typ}})
		_, _ = io.WriteString(t.w, "data: [DONE]\n\n")
		t.flush()
		return
	}
	t.w.Header().Set("Content-Type", "application/json")
	t.w.WriteHeader(status)
	_, _ = t.w.Write(clients.OpenAIErrorBody(msg, typ))
}

// passthrough meneruskan respons error dari 9Router.
func (t *toolWriter) passthrough(status int, raw []byte) {
	if t.stream {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error.Message == "" {
			e.Error.Message = fmt.Sprintf("9Router menjawab %d: %s", status, strings.TrimSpace(string(raw[:min(len(raw), 200)])))
		}
		t.fail(status, e.Error.Message, e.Error.Type)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.w.Header().Set("Content-Type", "application/json")
	t.w.WriteHeader(status)
	_, _ = t.w.Write(raw)
}

// finish menutup jawaban: SSE (finish_reason, usage, [DONE]) atau JSON utuh
// berisi seluruh teks dan usage gabungan semua putaran.
func (t *toolWriter) finish(model, text, finish string, usage *openai.Usage, includeUsage bool) {
	if model != "" {
		t.model = model
	}
	if finish == "" || finish == "tool_calls" {
		finish = "stop"
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.stream {
		resp := map[string]any{
			"id": t.id, "object": "chat.completion", "created": t.created, "model": t.model,
			"choices": []any{map[string]any{
				"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": finish,
			}},
		}
		if usage != nil {
			resp["usage"] = usageJSON(usage)
		}
		b, _ := json.Marshal(resp)
		t.w.Header().Set("Content-Type", "application/json")
		t.w.WriteHeader(http.StatusOK)
		_, _ = t.w.Write(b)
		return
	}

	if !t.started { // model tidak menulis teks apa pun
		t.event(t.chunk([]any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil}}))
		t.started = true
	}
	t.event(t.chunk([]any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}))
	if includeUsage && usage != nil {
		c := t.chunk([]any{})
		c["usage"] = usageJSON(usage)
		t.event(c)
	}
	_, _ = io.WriteString(t.w, "data: [DONE]\n\n")
	t.flush()
}

func usageJSON(u *openai.Usage) map[string]any {
	m := map[string]any{"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens, "total_tokens": u.TotalTokens}
	if u.CachedTokens > 0 {
		m["prompt_tokens_details"] = map[string]int{"cached_tokens": u.CachedTokens}
	}
	return m
}
