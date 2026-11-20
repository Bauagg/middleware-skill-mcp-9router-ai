// Package mcp menghubungkan middleware ke server MCP (Figma, Canva, dll.).
// 9Router tidak bisa memakai MCP, jadi middleware yang menjadi klien MCP:
//
//  1. daftar server dibaca dari file MCP_CONFIG_PATH; login OAuth dilakukan
//     sekali per server lewat browser (satu akun bersama), tokennya disimpan
//     terenkripsi di m_mcp_credentials dan diperpanjang otomatis;
//  2. daftar tool tiap server diambil dan di-index (BM25);
//  3. untuk setiap pesan, hanya tool yang cocok (maks MCP_MAX_TOOLS) yang
//     dikirim ke model; bila model memanggil tool, middleware menjalankannya
//     di server MCP dan mengembalikan hasilnya ke model (lihat proxy).
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/bm25"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/secretbox"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/tokens"
)

const (
	listTimeout   = 30 * time.Second
	loginTimeout  = 30 * time.Second
	descMaxRunes  = 1500 // deskripsi tool yang dikirim ke model dipotong supaya hemat token
	expirySkew    = time.Minute
	clientName    = "middleware-skill-ai"
	serverWeight  = 3 // bobot nama server saat mencocokkan tool
	keywordWeight = 2
	toolWeight    = 2
)

// Options adalah pengaturan Service.
type Options struct {
	MaxTools        int
	MinScore        float64
	ToolTimeout     time.Duration
	ResultMaxTokens int
	RefreshInterval time.Duration
	BaseURL         string // alamat publik middleware, untuk redirect login OAuth
}

// Service mengelola koneksi ke server MCP dan index tool-nya.
type Service struct {
	repo *Repository
	box  *secretbox.Box
	opt  Options

	servers map[string]*server
	names   []string // urutan stabil

	snap atomic.Pointer[snapshot]
}

// server adalah satu server MCP dari file konfigurasi beserta statusnya.
type server struct {
	name   string
	cfg    ServerConfig
	client *clients.MCPClient
	tokens *tokenHandler

	mu          sync.Mutex
	state       string
	err         string
	tools       []clients.MCPTool
	refreshedAt *time.Time
}

// toolEntry adalah satu tool di index.
type toolEntry struct {
	name   string // nama untuk model: <server>__<tool>
	server *server
	tool   clients.MCPTool
	spec   json.RawMessage
}

type snapshot struct {
	entries []*toolEntry
	byName  map[string]*toolEntry
	corpus  *bm25.Corpus
}

// NewService membuat Service dari isi file konfigurasi. box boleh nil bila
// tidak ada server yang aktif.
func NewService(repo *Repository, box *secretbox.Box, cfg *FileConfig, opt Options) *Service {
	s := &Service{repo: repo, box: box, opt: opt, servers: map[string]*server{}}

	for name, sc := range cfg.Servers {
		srv := &server{name: name, cfg: sc, state: StateDisabled}
		if sc.IsEnabled() {
			srv.state = StateConnecting
			srv.tokens = &tokenHandler{svc: s, server: name}
			srv.client = clients.NewMCPClient(name, sc.URL, expandHeaders(sc.Headers), srv.tokens)
		}
		s.servers[name] = srv
		s.names = append(s.names, name)
	}
	sort.Strings(s.names)
	s.snap.Store(&snapshot{byName: map[string]*toolEntry{}})
	return s
}

// Enabled melaporkan apakah ada server MCP yang aktif.
func (s *Service) Enabled() bool {
	for _, srv := range s.servers {
		if srv.client != nil {
			return true
		}
	}
	return false
}

// Run mengambil daftar tool semua server saat start, lalu mengulanginya tiap
// RefreshInterval. Server yang gagal tidak menghentikan yang lain.
func (s *Service) Run(ctx context.Context) {
	s.refreshAll(ctx)
	if s.opt.RefreshInterval <= 0 {
		return
	}
	t := time.NewTicker(s.opt.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.refreshAll(ctx)
		}
	}
}

func (s *Service) refreshAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, srv := range s.servers {
		if srv.client == nil {
			continue
		}
		wg.Add(1)
		go func(srv *server) {
			defer wg.Done()
			s.refresh(ctx, srv)
		}(srv)
	}
	wg.Wait()
	s.rebuild()
}

// refresh mengambil daftar tool satu server dan memperbarui statusnya.
func (s *Service) refresh(ctx context.Context, srv *server) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	tools, err := srv.client.ListTools(ctx)
	now := time.Now()

	srv.mu.Lock()
	defer srv.mu.Unlock()
	switch {
	case err == nil:
		srv.state, srv.err, srv.tools, srv.refreshedAt = StateConnected, "", tools, &now
		logger.Info("mcp terhubung", zap.String("server", srv.name), zap.Int("tools", len(tools)))
	case errors.Is(err, clients.ErrMCPNeedsLogin):
		srv.state, srv.err, srv.tools = StateNeedsLogin, "", nil
		logger.Info("mcp butuh login", zap.String("server", srv.name), zap.String("connect", s.connectURL(srv.name)))
	default:
		// daftar tool lama tetap dipakai bila gagal sesaat
		srv.state, srv.err = StateError, err.Error()
		logger.Warn("mcp gagal mengambil daftar tool", zap.String("server", srv.name), zap.Error(err))
	}
}

// rebuild menyusun ulang index tool dari semua server yang terhubung, lalu
// menukarnya sekaligus (request yang sedang berjalan memakai index lama).
func (s *Service) rebuild() {
	snap := &snapshot{byName: map[string]*toolEntry{}}
	var docs [][]string

	for _, name := range s.names {
		srv := s.servers[name]
		srv.mu.Lock()
		tools := srv.tools
		srv.mu.Unlock()

		for _, t := range tools {
			e := &toolEntry{name: toolName(srv.name, t.Name), server: srv, tool: t}
			if _, dup := snap.byName[e.name]; dup {
				continue
			}
			e.spec = buildSpec(e.name, srv.name, t)
			snap.entries = append(snap.entries, e)
			snap.byName[e.name] = e

			var doc []string
			for range serverWeight {
				doc = append(doc, bm25.Tokenize(srv.name)...)
			}
			for range keywordWeight {
				doc = append(doc, bm25.Tokenize(strings.Join(srv.cfg.Keywords, " "))...)
			}
			for range toolWeight {
				doc = append(doc, bm25.Tokenize(t.Name+" "+t.Title)...)
			}
			doc = append(doc, bm25.Tokenize(t.Description)...)
			docs = append(docs, doc)
		}
	}
	if len(docs) > 0 {
		snap.corpus = bm25.New(docs)
	}
	s.snap.Store(snap)
}

// ---------------------------------------------------------------------------
// dipakai proxy
// ---------------------------------------------------------------------------

// Select memilih tool yang cocok dengan pesan user (maks MaxTools, skor
// minimal MinScore). Pesan yang tidak menyinggung tool apa pun tidak membawa
// tool sama sekali, jadi tidak menambah token.
func (s *Service) Select(_ context.Context, query string) []ToolSpec {
	if s.opt.MaxTools <= 0 {
		return nil
	}
	hits := s.score(query)
	var out []ToolSpec
	for _, h := range hits {
		if h.score < s.opt.MinScore || len(out) == s.opt.MaxTools {
			break
		}
		out = append(out, ToolSpec{Name: h.entry.name, Server: h.entry.server.name, Definition: h.entry.spec})
	}
	return out
}

// Call menjalankan tool yang diminta model. Hasilnya selalu berupa teks untuk
// model; kegagalan juga dilaporkan sebagai teks (IsError) supaya model bisa
// memperbaiki argumennya atau menjelaskan ke user.
func (s *Service) Call(ctx context.Context, name, args string) CallResult {
	e := s.snap.Load().byName[name]
	if e == nil {
		return CallResult{Text: "tool " + name + " tidak dikenal atau servernya sedang tidak terhubung", IsError: true}
	}

	ctx, cancel := context.WithTimeout(ctx, s.opt.ToolTimeout)
	defer cancel()

	start := time.Now()
	res, err := e.server.client.CallTool(ctx, e.tool.Name, json.RawMessage(strings.TrimSpace(args)))
	if err != nil {
		if errors.Is(err, clients.ErrMCPNeedsLogin) {
			e.server.mu.Lock()
			e.server.state = StateNeedsLogin
			e.server.mu.Unlock()
		}
		logger.Warn("tool mcp gagal", zap.String("tool", name), zap.Duration("latency", time.Since(start)), zap.Error(err))
		return CallResult{Text: "gagal memanggil tool: " + err.Error(), IsError: true}
	}

	text := res.Text
	if s.opt.ResultMaxTokens > 0 {
		if cut, ok := tokens.Truncate(text, s.opt.ResultMaxTokens); ok {
			text = cut + "\n[... hasil tool dipotong karena batas token]"
		}
	}
	if strings.TrimSpace(text) == "" {
		text = "(tool selesai tanpa keluaran)"
	}
	logger.Info("tool mcp dipanggil", zap.String("tool", name), zap.Bool("is_error", res.IsError),
		zap.Duration("latency", time.Since(start)), zap.Int("result_tokens_est", tokens.Estimate(text)))
	return CallResult{Text: text, IsError: res.IsError}
}

type scored struct {
	entry *toolEntry
	score float64
}

func (s *Service) score(query string) []scored {
	snap := s.snap.Load()
	if snap.corpus == nil {
		return nil
	}
	var hits []scored
	for i, sc := range snap.corpus.Score(bm25.Tokenize(query)) {
		if sc > 0 {
			hits = append(hits, scored{entry: snap.entries[i], score: sc})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	return hits
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

// Statuses mengembalikan status semua server di file konfigurasi.
func (s *Service) Statuses(ctx context.Context) []ServerStatus {
	out := make([]ServerStatus, 0, len(s.names))
	for _, name := range s.names {
		srv := s.servers[name]
		srv.mu.Lock()
		st := ServerStatus{
			Name: name, URL: srv.cfg.URL, State: srv.state, Error: srv.err,
			Tools: len(srv.tools), RefreshedAt: srv.refreshedAt, ConnectURL: s.connectURL(name),
		}
		for _, t := range srv.tools {
			if t.ReadOnly {
				st.ReadOnlyTools++
			}
		}
		srv.mu.Unlock()

		if cred, err := s.repo.GetCredential(ctx, name); err == nil && cred != nil && cred.AccessTokenEnc != "" {
			st.LoggedIn, st.TokenExpires = true, cred.ExpiresAt
		}
		out = append(out, st)
	}
	return out
}

// Tools menampilkan tool yang terhubung. Dengan q, diurutkan menurut skor dan
// ditandai mana yang akan dikirim ke model untuk pesan q.
func (s *Service) Tools(q string, limit int) []ToolInfo {
	info := func(e *toolEntry) ToolInfo {
		return ToolInfo{Name: e.name, Server: e.server.name, Tool: e.tool.Name, Title: e.tool.Title,
			Description: clipRunes(e.tool.Description, 300), ReadOnly: e.tool.ReadOnly}
	}

	var out []ToolInfo
	if strings.TrimSpace(q) == "" {
		for _, e := range s.snap.Load().entries {
			out = append(out, info(e))
		}
	} else {
		selected := map[string]bool{}
		for _, t := range s.Select(context.Background(), q) {
			selected[t.Name] = true
		}
		for _, h := range s.score(q) {
			ti := info(h.entry)
			sc, sel := h.score, selected[h.entry.name]
			ti.Score, ti.Selected = &sc, &sel
			out = append(out, ti)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []ToolInfo{}
	}
	return out
}

// Refresh mengambil ulang daftar tool satu server.
func (s *Service) Refresh(ctx context.Context, name string) (*ServerStatus, error) {
	srv, err := s.enabledServer(name)
	if err != nil {
		return nil, err
	}
	srv.tokens.invalidate()
	s.refresh(ctx, srv)
	s.rebuild()
	return s.status(ctx, name), nil
}

// StartLogin menyiapkan login OAuth dan mengembalikan alamat halaman login
// yang dibuka di browser. Klien OAuth didaftarkan otomatis (DCR) bila belum.
func (s *Service) StartLogin(ctx context.Context, name string) (string, error) {
	srv, err := s.enabledServer(name)
	if err != nil {
		return "", err
	}
	if s.box == nil {
		return "", apperror.NewInternalServerError("MCP_TOKEN_KEY belum diisi")
	}
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	info, err := clients.DiscoverMCPAuth(ctx, srv.cfg.URL)
	if err != nil {
		return "", apperror.NewBadRequest(name + ": " + err.Error())
	}

	cred, err := s.repo.GetCredential(ctx, name)
	if err != nil {
		return "", err
	}
	if cred == nil {
		cred = &Credential{Server: name}
	}

	redirect := s.redirectURL()
	if cred.ClientID == "" || cred.RedirectURI != redirect || cred.AuthURL != info.AuthURL || cred.TokenURL != info.TokenURL {
		reg, err := clients.RegisterMCPClient(ctx, info, redirect, clientName)
		if err != nil {
			return "", apperror.NewBadRequest(name + ": " + err.Error())
		}
		secret, err := s.box.Seal(reg.ClientSecret)
		if err != nil {
			return "", err
		}
		cred.ClientID, cred.ClientSecretEnc, cred.AuthMethod, cred.RedirectURI = reg.ClientID, secret, reg.AuthMethod, redirect
		logger.Info("klien oauth mcp didaftarkan", zap.String("server", name), zap.String("auth_method", reg.AuthMethod))
	}
	cred.AuthURL, cred.TokenURL, cred.Resource, cred.Scopes = info.AuthURL, info.TokenURL, info.Resource, strings.Join(info.Scopes, " ")
	if err := s.repo.SaveCredential(ctx, cred); err != nil {
		return "", err
	}

	conf, err := s.oauthConfig(cred)
	if err != nil {
		return "", err
	}
	state := randomHex(24)
	verifier := oauth2.GenerateVerifier()
	if err := s.repo.SaveState(ctx, state, oauthState{Server: name, Verifier: verifier}); err != nil {
		return "", apperror.NewInternalServerError("Gagal menyimpan state login (Redis): " + err.Error())
	}

	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if cred.Resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", cred.Resource))
	}
	return conf.AuthCodeURL(state, opts...), nil
}

// FinishLogin menukar kode dari halaman login menjadi token, menyimpannya,
// lalu langsung mengambil daftar tool server itu.
func (s *Service) FinishLogin(ctx context.Context, state, code string) (*ServerStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	st, err := s.repo.TakeState(ctx, state)
	if err != nil {
		return nil, apperror.NewBadRequest(err.Error())
	}
	srv, err := s.enabledServer(st.Server)
	if err != nil {
		return nil, err
	}
	cred, err := s.repo.GetCredential(ctx, st.Server)
	if err != nil || cred == nil {
		return nil, apperror.NewBadRequest("pendaftaran klien OAuth tidak ditemukan, ulangi dari /connect")
	}
	conf, err := s.oauthConfig(cred)
	if err != nil {
		return nil, err
	}

	opts := []oauth2.AuthCodeOption{oauth2.VerifierOption(st.Verifier)}
	if cred.Resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", cred.Resource))
	}
	tok, err := conf.Exchange(context.WithValue(ctx, oauth2.HTTPClient, tokenHTTP), code, opts...)
	if err != nil {
		return nil, apperror.NewBadRequest(st.Server + ": menukar kode login gagal: " + err.Error())
	}
	if err := s.saveToken(ctx, st.Server, tok, true); err != nil {
		return nil, err
	}

	srv.tokens.invalidate()
	srv.client.Close()
	s.refresh(ctx, srv)
	s.rebuild()
	return s.status(ctx, st.Server), nil
}

// Logout menghapus token login server.
func (s *Service) Logout(ctx context.Context, name string) (*ServerStatus, error) {
	srv, err := s.enabledServer(name)
	if err != nil {
		return nil, err
	}
	if err := s.repo.ClearTokens(ctx, name); err != nil {
		return nil, err
	}
	srv.tokens.invalidate()
	srv.client.Close()
	srv.mu.Lock()
	srv.state, srv.err, srv.tools = StateNeedsLogin, "", nil
	srv.mu.Unlock()
	s.rebuild()
	return s.status(ctx, name), nil
}

func (s *Service) status(ctx context.Context, name string) *ServerStatus {
	for _, st := range s.Statuses(ctx) {
		if st.Name == name {
			return &st
		}
	}
	return nil
}

func (s *Service) enabledServer(name string) (*server, error) {
	srv := s.servers[name]
	if srv == nil {
		return nil, apperror.NewNotFound("Server MCP " + name + " tidak ada di file konfigurasi")
	}
	if srv.client == nil {
		return nil, apperror.NewBadRequest("Server MCP " + name + " dimatikan (enabled: false)")
	}
	return srv, nil
}

func (s *Service) redirectURL() string {
	return strings.TrimRight(s.opt.BaseURL, "/") + "/api/mcp/oauth/callback"
}

func (s *Service) connectURL(name string) string {
	return strings.TrimRight(s.opt.BaseURL, "/") + "/api/mcp/servers/" + url.PathEscape(name) + "/connect"
}

// ---------------------------------------------------------------------------
// token OAuth
// ---------------------------------------------------------------------------

var tokenHTTP = &http.Client{Timeout: 20 * time.Second}

func (s *Service) oauthConfig(cred *Credential) (*oauth2.Config, error) {
	secret, err := s.box.Open(cred.ClientSecretEnc)
	if err != nil {
		return nil, err
	}
	style := oauth2.AuthStyleInParams
	if cred.AuthMethod == "client_secret_basic" {
		style = oauth2.AuthStyleInHeader
	}
	var scopes []string
	if cred.Scopes != "" {
		scopes = strings.Fields(cred.Scopes)
	}
	return &oauth2.Config{
		ClientID:     cred.ClientID,
		ClientSecret: secret,
		Endpoint:     oauth2.Endpoint{AuthURL: cred.AuthURL, TokenURL: cred.TokenURL, AuthStyle: style},
		RedirectURL:  cred.RedirectURI,
		Scopes:       scopes,
	}, nil
}

// saveToken menyimpan token (terenkripsi). Refresh token lama dipertahankan
// bila server tidak mengirim yang baru.
func (s *Service) saveToken(ctx context.Context, server string, tok *oauth2.Token, fresh bool) error {
	cred, err := s.repo.GetCredential(ctx, server)
	if err != nil {
		return err
	}
	if cred == nil {
		return errors.New("kredensial " + server + " tidak ditemukan")
	}

	access, err := s.box.Seal(tok.AccessToken)
	if err != nil {
		return err
	}
	cred.AccessTokenEnc, cred.TokenType = access, tok.TokenType
	if tok.RefreshToken != "" {
		if cred.RefreshTokenEnc, err = s.box.Seal(tok.RefreshToken); err != nil {
			return err
		}
	}
	cred.ExpiresAt = nil
	if !tok.Expiry.IsZero() {
		exp := tok.Expiry
		cred.ExpiresAt = &exp
	}
	if fresh {
		now := time.Now()
		cred.ConnectedAt = &now
	}
	return s.repo.SaveCredential(ctx, cred)
}

// tokenHandler memenuhi auth.OAuthHandler dari SDK MCP: memberi token untuk
// setiap request ke server, memperpanjangnya bila kedaluwarsa, dan menandai
// server "butuh login" bila ditolak.
type tokenHandler struct {
	svc    *Service
	server string

	mu     sync.Mutex
	loaded bool
	ts     oauth2.TokenSource
}

// TokenSource mengembalikan sumber token; nil berarti belum login (request
// dikirim tanpa header Authorization dari OAuth).
func (h *tokenHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.loaded {
		return h.ts, nil
	}

	h.loaded, h.ts = true, nil
	if h.svc.box == nil {
		return nil, nil
	}
	cred, err := h.svc.repo.GetCredential(ctx, h.server)
	if err != nil {
		h.loaded = false
		return nil, err
	}
	if cred == nil || cred.AccessTokenEnc == "" {
		return nil, nil
	}

	access, err := h.svc.box.Open(cred.AccessTokenEnc)
	if err != nil {
		return nil, err
	}
	refresh, err := h.svc.box.Open(cred.RefreshTokenEnc)
	if err != nil {
		return nil, err
	}
	conf, err := h.svc.oauthConfig(cred)
	if err != nil {
		return nil, err
	}

	tok := &oauth2.Token{AccessToken: access, RefreshToken: refresh, TokenType: cred.TokenType}
	if cred.ExpiresAt != nil {
		tok.Expiry = *cred.ExpiresAt
	}
	// konteks latar belakang: token source dipakai lintas request
	base := conf.TokenSource(context.WithValue(context.Background(), oauth2.HTTPClient, tokenHTTP), tok)
	h.ts = &persistingSource{base: base, last: access, save: func(t *oauth2.Token) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.svc.saveToken(ctx, h.server, t, false); err != nil {
			logger.Warn("simpan token mcp yang diperpanjang gagal", zap.String("server", h.server), zap.Error(err))
		} else {
			logger.Info("token mcp diperpanjang", zap.String("server", h.server))
		}
	}}
	return h.ts, nil
}

// Authorize dipanggil SDK saat server menjawab 401/403. Login butuh browser,
// jadi tidak bisa dilakukan di tengah request: server ditandai butuh login.
func (h *tokenHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	return clients.ErrMCPNeedsLogin
}

func (h *tokenHandler) invalidate() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.loaded, h.ts = false, nil
	h.mu.Unlock()
}

// persistingSource menyimpan token baru ke database setiap kali diperpanjang.
type persistingSource struct {
	base oauth2.TokenSource
	save func(*oauth2.Token)

	mu   sync.Mutex
	last string
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	t, err := p.base.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: perpanjang token gagal: %v", clients.ErrMCPNeedsLogin, err)
	}
	p.mu.Lock()
	changed := t.AccessToken != p.last
	p.last = t.AccessToken
	p.mu.Unlock()
	if changed {
		p.save(t)
	}
	return t, nil
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

// toolName membuat nama function yang valid untuk model (huruf, angka, _ dan
// -, maks 64): <server>__<tool>. Nama yang terlalu panjang diberi akhiran hash
// supaya tetap unik.
func toolName(server, tool string) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
				return r
			}
			return '_'
		}, s)
	}
	name := clean(server) + "__" + clean(tool)
	if len(name) <= 64 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return name[:55] + "_" + hex.EncodeToString(sum[:4])
}

// buildSpec menyusun definisi function OpenAI dari tool MCP.
func buildSpec(name, server string, t clients.MCPTool) json.RawMessage {
	desc := t.Description
	if t.Title != "" && !strings.Contains(desc, t.Title) {
		desc = t.Title + ". " + desc
	}
	desc = "[" + server + "] " + clipRunes(strings.TrimSpace(desc), descMaxRunes)

	b, _ := json.Marshal(map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": desc,
			"parameters":  normalizeSchema(t.InputSchema),
		},
	})
	return b
}

// normalizeSchema memastikan parameter berupa objek JSON Schema yang diterima
// provider: tipe object, properties ada, dan tanpa field $schema.
func normalizeSchema(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	delete(m, "$schema")
	if m["type"] == nil {
		m["type"] = "object"
	}
	if _, ok := m["properties"]; !ok {
		m["properties"] = map[string]any{}
	}
	return m
}

// expandHeaders mengganti ${NAMA_ENV} di nilai header dengan isi env.
func expandHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = os.Expand(v, os.Getenv)
	}
	return out
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
