// Package proxy meneruskan request OpenAI-compatible ke 9Router. Untuk
// /v1/chat/completions, konteks tambahan disisipkan lebih dulu ke awal system
// prompt dengan urutan tetap: skill yang relevan, context pack, lalu catatan
// memori yang cocok dengan pesan user. Bila ada tool MCP yang cocok, tool itu
// ikut dikirim dan middleware menjalankan putaran panggil-tool (lihat RunWithTools).
package proxy

import (
	"context"
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
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/modelbudget"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/openai"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/tokens"
	"github.com/Bauagg/middleware-skill-ai/internal/services/contextpack"
	"github.com/Bauagg/middleware-skill-ai/internal/services/mcp"
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
// Chat dipakai putaran tool MCP (tanpa streaming, jawaban dibaca utuh).
type Upstream interface {
	Forward(w http.ResponseWriter, r *http.Request) clients.Result
	Chat(ctx context.Context, authorization string, body []byte) (int, []byte, error)
}

// Tools memilih dan menjalankan tool MCP; mcp.Service memenuhinya.
type Tools interface {
	Select(ctx context.Context, query string) []mcp.ToolSpec
	Call(ctx context.Context, name, args string) mcp.CallResult
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
	Observe(userText string, recent []memorynote.Turn)
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
	Tools        Tools        // boleh nil (MCP dimatikan)
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

// RecordUsage mencatat satu request chat completions yang sudah selesai.
func (s *Service) RecordUsage(p *Prepared, res clients.Result, elapsed time.Duration) {
	if s.Recorder == nil {
		return
	}

	e := usagelog.Entry{
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
func (s *Service) Learn(p *Prepared) {
	if s.Memory == nil || p.Query == "" {
		return
	}
	s.Memory.Observe(p.Query, p.Recent)
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
	p := &Prepared{Body: raw}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return p, fmt.Errorf("body bukan JSON: %w", err)
	}
	_ = json.Unmarshal(body["model"], &p.Model)
	_ = json.Unmarshal(body["stream"], &p.Stream)
	p.BudgetTokens = s.Budgets.For(p.Model)

	// Standar OpenAI: stream bawaannya false. 9Router justru menjawab SSE bila
	// field ini tidak ada, sehingga SDK OpenAI yang tidak mengirimnya menerima
	// SSE padahal menunggu JSON. Jadi nilainya ditulis eksplisit.
	if _, ok := body["stream"]; !ok {
		body["stream"] = json.RawMessage("false")
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
	convKey := skillcache.ConversationKey(meta.Authorization, meta.ConversationID, firstUserText(messages))

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
			return strings.TrimSpace(contentText(messages[i]["content"]))
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
		if text := strings.TrimSpace(contentText(messages[i]["content"])); text != "" {
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
			if t := strings.TrimSpace(contentText(messages[i]["content"])); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
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
			return strings.TrimSpace(contentText(m["content"]))
		}
	}
	return ""
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
// putaran tool MCP
// ---------------------------------------------------------------------------

// keepaliveInterval: selama tool berjalan, client streaming dikirimi komentar
// SSE supaya koneksinya tidak dianggap mati oleh client atau proxy di depan.
const keepaliveInterval = 10 * time.Second

// toolCall adalah satu permintaan tool dari model.
type toolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// RunWithTools menjalankan request yang membawa tool MCP:
//
//  1. kirim ke 9Router tanpa streaming;
//  2. bila model meminta tool, jalankan semua tool itu di server MCP
//     (bersamaan), tambahkan hasilnya ke percakapan, lalu kirim lagi;
//  3. ulangi sampai model menjawab tanpa meminta tool, atau batas
//     ToolMaxSteps tercapai (putaran terakhir memakai tool_choice "none");
//  4. kirim jawaban akhir ke client: JSON biasa, atau SSE bila client
//     meminta stream (jawaban dikirim sekaligus, bukan per kata).
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
	body["stream"] = json.RawMessage("false")
	delete(body, "stream_options")

	out := newToolWriter(w, p.Stream)
	defer out.stop()

	maxSteps := max(s.ToolMaxSteps, 1)
	var total openai.Usage
	hasUsage := false

	for step := 1; ; step++ {
		last := step > maxSteps
		if last {
			body["tool_choice"] = json.RawMessage(`"none"`)
		}
		body["messages"], _ = json.Marshal(messages)
		reqBody, _ := json.Marshal(body)

		status, raw, err := s.Upstream.Chat(ctx, r.Header.Get("Authorization"), reqBody)
		p.ToolSteps = step
		if err != nil {
			if ctx.Err() != nil {
				res.Aborted = true
				return res
			}
			logger.Error("putaran tool: 9Router gagal", zap.Error(err))
			res.Status = http.StatusBadGateway
			out.fail(res.Status, "9Router tidak dapat dihubungi", "bad_gateway")
			return res
		}

		sn := openai.NewUsageSniffer("application/json")
		_, _ = sn.Write(raw)
		if u := sn.Usage(); u != nil {
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

		if status/100 != 2 {
			res.Status = status
			out.passthrough(status, raw)
			return res
		}

		msg, calls, ok := parseChoice(raw)
		if !ok {
			res.Status = http.StatusBadGateway
			out.fail(res.Status, "respons 9Router tidak dikenali", "bad_gateway")
			return res
		}
		if len(calls) == 0 || last {
			res.Status = http.StatusOK
			out.final(raw, res.Usage, includeUsage)
			return res
		}

		// jalankan tool yang diminta, lalu lanjutkan percakapan dengan hasilnya
		messages = append(messages, msg)
		names := make([]string, len(calls))
		for i, c := range calls {
			names[i] = c.Function.Name
		}
		out.comment("menjalankan tool: " + strings.Join(names, ", "))

		results := make([]mcp.CallResult, len(calls))
		var wg sync.WaitGroup
		for i, c := range calls {
			wg.Add(1)
			go func(i int, c toolCall) {
				defer wg.Done()
				results[i] = s.Tools.Call(ctx, c.Function.Name, c.Function.Arguments)
			}(i, c)
		}
		wg.Wait()

		for i, c := range calls {
			text := results[i].Text
			if results[i].IsError {
				text = "ERROR: " + text
			}
			m, _ := json.Marshal(map[string]string{"role": "tool", "tool_call_id": c.ID, "content": text})
			messages = append(messages, m)
			p.ToolCalls = append(p.ToolCalls, c.Function.Name)
		}
	}
}

// parseChoice membaca pesan jawaban pertama beserta tool yang diminta.
// msg dikembalikan utuh (termasuk field tambahan seperti reasoning_content)
// supaya bisa dikirim balik ke model apa adanya.
func parseChoice(raw []byte) (msg json.RawMessage, calls []toolCall, ok bool) {
	var resp struct {
		Choices []struct {
			Message json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &resp) != nil || len(resp.Choices) == 0 || len(resp.Choices[0].Message) == 0 {
		return nil, nil, false
	}
	msg = resp.Choices[0].Message

	var m struct {
		ToolCalls []toolCall `json:"tool_calls"`
	}
	_ = json.Unmarshal(msg, &m)
	for _, c := range m.ToolCalls {
		if c.Function.Name != "" {
			calls = append(calls, c)
		}
	}
	return msg, calls, true
}

// streamIncludeUsage membaca stream_options.include_usage dari request.
func streamIncludeUsage(raw json.RawMessage) bool {
	var o struct {
		IncludeUsage bool `json:"include_usage"`
	}
	_ = json.Unmarshal(raw, &o)
	return o.IncludeUsage
}

// toolWriter menulis hasil putaran tool ke client, dalam bentuk JSON biasa
// atau SSE (bila client meminta stream).
type toolWriter struct {
	w      http.ResponseWriter
	stream bool

	mu   sync.Mutex
	done chan struct{}
}

func newToolWriter(w http.ResponseWriter, stream bool) *toolWriter {
	t := &toolWriter{w: w, stream: stream, done: make(chan struct{})}
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
			e.Error.Message = fmt.Sprintf("9Router menjawab %d", status)
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

// final mengirim jawaban akhir dengan usage gabungan semua putaran.
func (t *toolWriter) final(raw []byte, usage *openai.Usage, includeUsage bool) {
	var resp map[string]json.RawMessage
	_ = json.Unmarshal(raw, &resp)
	if usage != nil {
		resp["usage"], _ = json.Marshal(usageJSON(usage))
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.stream {
		b, _ := json.Marshal(resp)
		t.w.Header().Set("Content-Type", "application/json")
		t.w.WriteHeader(http.StatusOK)
		_, _ = t.w.Write(b)
		return
	}

	// ubah jawaban utuh menjadi potongan SSE berformat chat.completion.chunk
	var head struct {
		ID      string `json:"id"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.Unmarshal(raw, &head)
	text, finish := "", "stop"
	if len(head.Choices) > 0 {
		text = contentText(head.Choices[0].Message.Content)
		if fr := head.Choices[0].FinishReason; fr != "" && fr != "tool_calls" {
			finish = fr
		}
	}
	if head.Created == 0 {
		head.Created = time.Now().Unix()
	}
	chunk := func(choices any) map[string]any {
		return map[string]any{"id": head.ID, "object": "chat.completion.chunk", "created": head.Created, "model": head.Model, "choices": choices}
	}

	t.event(chunk([]any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": text}, "finish_reason": nil}}))
	t.event(chunk([]any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}))
	if includeUsage && usage != nil {
		c := chunk([]any{})
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
