// Package mcp menghubungkan middleware ke server MCP (Canva, Notion, dll.).
// 9Router tidak bisa memakai MCP, jadi middleware yang menjadi klien MCP,
// terpisah untuk tiap user (header X-User-Id):
//
//  1. server yang terlihat oleh user = server bawaan di mcp.json (untuk semua
//     user) + server yang ditambahkan user itu sendiri (tabel m_mcp_servers);
//  2. tiap user login OAuth ke akunnya sendiri lewat browser; tokennya
//     disimpan terenkripsi di m_mcp_credentials per (user, server) dan
//     diperpanjang otomatis;
//  3. koneksi dan daftar tool dibuat per (user, server) saat pertama dipakai;
//  4. untuk setiap pesan, hanya tool milik user yang cocok (maks
//     MCP_MAX_TOOLS) yang dikirim ke model; bila model memanggil tool,
//     middleware menjalankannya dengan akun user itu (lihat proxy).
package mcp

import (
	"context"
	"crypto/hmac"
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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/bm25"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/llmtool"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/secretbox"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/tokens"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

const (
	listTimeout      = 30 * time.Second
	firstListTimeout = 10 * time.Second // percobaan pertama saat chat: jangan menahan jawaban lama
	loginTimeout     = 30 * time.Second
	descMaxRunes     = 1500 // deskripsi tool yang dikirim ke model dipotong supaya hemat token
	clientName       = "middleware-skill-ai"
	serverWeight     = 3 // bobot nama server saat mencocokkan tool
	keywordWeight    = 2
	toolWeight       = 2

	userDefsTTL = time.Minute    // cache daftar server milik user
	idleTTL     = time.Hour      // koneksi yang tidak dipakai selama ini ditutup
	linkTTL     = 24 * time.Hour // masa berlaku link login yang dibagikan di chat
)

// Options adalah pengaturan Service.
type Options struct {
	MaxTools        int
	MinScore        float64
	ToolTimeout     time.Duration
	ResultMaxTokens int
	RefreshInterval time.Duration
	BaseURL         string // alamat publik middleware, untuk redirect login OAuth
	LinkKey         []byte // kunci tanda tangan link login (kosong = link butuh header X-User-Id)
}

// serverDef adalah satu server yang terlihat oleh seorang user.
type serverDef struct {
	Name string
	Cfg  ServerConfig
	Own  bool // true = milik user (m_mcp_servers), false = bawaan mcp.json
}

// conn adalah koneksi satu user ke satu server beserta statusnya.
type conn struct {
	user, name string
	cfg        ServerConfig
	client     *clients.MCPClient
	tokens     *tokenHandler

	mu          sync.Mutex
	state       string
	err         string
	tools       []clients.MCPTool
	refreshedAt *time.Time
	tried       bool
	refreshing  bool
	lastUsed    time.Time
}

// toolEntry adalah satu tool di index milik seorang user.
type toolEntry struct {
	name string // nama untuk model: <server>__<tool>
	conn *conn
	tool clients.MCPTool
	spec json.RawMessage
}

type index struct {
	entries []*toolEntry
	byName  map[string]*toolEntry
	corpus  *bm25.Corpus
}

type userDefsEntry struct {
	defs []serverDef
	at   time.Time
}

// Service mengelola server MCP, koneksi, dan login per user.
type Service struct {
	repo *Repository
	box  *secretbox.Box
	opt  Options

	global []serverDef // dari mcp.json, terurut menurut nama

	mu       sync.Mutex
	conns    map[string]*conn // kunci: user \x00 server
	userDefs map[string]userDefsEntry
}

// NewService membuat Service dari isi mcp.json (server bawaan). box boleh nil
// bila MCP_TOKEN_KEY kosong (login OAuth tidak bisa dipakai).
func NewService(repo *Repository, box *secretbox.Box, cfg *FileConfig, opt Options) *Service {
	s := &Service{repo: repo, box: box, opt: opt, conns: map[string]*conn{}, userDefs: map[string]userDefsEntry{}}
	for name, sc := range cfg.Servers {
		s.global = append(s.global, serverDef{Name: name, Cfg: sc})
	}
	sort.Slice(s.global, func(i, j int) bool { return s.global[i].Name < s.global[j].Name })
	return s
}

// Run menutup koneksi yang lama tidak dipakai, supaya session MCP tidak
// menumpuk untuk user yang sudah tidak aktif.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			for k, c := range s.conns {
				c.mu.Lock()
				idle := time.Since(c.lastUsed) > idleTTL
				c.mu.Unlock()
				if idle {
					c.client.Close()
					delete(s.conns, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

func connKey(user, name string) string { return user + "\x00" + name }

// defs mengembalikan server yang terlihat oleh user: bawaan + milik user.
func (s *Service) defs(ctx context.Context, user string) []serverDef {
	s.mu.Lock()
	e, cached := s.userDefs[user]
	s.mu.Unlock()

	own := e.defs
	if !cached || time.Since(e.at) > userDefsTTL {
		rows, err := s.repo.ListUserServers(ctx, user)
		if err != nil {
			logger.Warn("baca server mcp milik user gagal", zap.String("user", user), zap.Error(err))
		} else {
			own = own[:0:0]
			for _, r := range rows {
				var kw []string
				_ = json.Unmarshal([]byte(r.Keywords), &kw)
				own = append(own, serverDef{Name: r.Name, Cfg: ServerConfig{URL: r.URL, Keywords: kw}, Own: true})
			}
			s.mu.Lock()
			s.userDefs[user] = userDefsEntry{defs: own, at: time.Now()}
			s.mu.Unlock()
		}
	}

	out := make([]serverDef, 0, len(s.global)+len(own))
	out = append(out, s.global...)
	return append(out, own...)
}

func (s *Service) invalidateDefs(user string) {
	s.mu.Lock()
	delete(s.userDefs, user)
	s.mu.Unlock()
}

func (s *Service) findDef(ctx context.Context, user, name string) (serverDef, bool) {
	for _, d := range s.defs(ctx, user) {
		if d.Name == name {
			return d, true
		}
	}
	return serverDef{}, false
}

// connFor mengambil (atau membuat) koneksi user ke server def.
func (s *Service) connFor(user string, d serverDef) *conn {
	key := connKey(user, d.Name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.conns[key]; c != nil && c.cfg.URL == d.Cfg.URL {
		c.mu.Lock()
		c.lastUsed = time.Now()
		c.mu.Unlock()
		return c
	} else if c != nil {
		c.client.Close() // URL server berubah
	}
	c := &conn{user: user, name: d.Name, cfg: d.Cfg, state: StateConnecting, lastUsed: time.Now()}
	c.tokens = &tokenHandler{svc: s, user: user, server: d.Name}
	c.client = clients.NewMCPClient(d.Name, d.Cfg.URL, expandHeaders(d.Cfg.Headers), c.tokens)
	s.conns[key] = c
	return c
}

// existing mengambil koneksi user yang sudah ada (tanpa membuat baru).
func (s *Service) existing(user string) []*conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*conn
	prefix := user + "\x00"
	for k, c := range s.conns {
		if strings.HasPrefix(k, prefix) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// ensure menyiapkan koneksi user ke semua server aktifnya. Server yang belum
// pernah dicoba diambil daftar tool-nya sekarang (bersamaan, dibatasi
// firstListTimeout); yang sudah lama diperbarui di latar belakang.
func (s *Service) ensure(ctx context.Context, user string) []*conn {
	var out []*conn
	var wg sync.WaitGroup
	for _, d := range s.defs(ctx, user) {
		if !d.Cfg.IsEnabled() {
			continue
		}
		c := s.connFor(user, d)
		c.mu.Lock()
		tried, busy := c.tried, c.refreshing
		stale := s.opt.RefreshInterval > 0 && (c.refreshedAt == nil || time.Since(*c.refreshedAt) > s.opt.RefreshInterval)
		c.mu.Unlock()
		switch {
		case !tried:
			wg.Add(1)
			go func(c *conn) {
				defer wg.Done()
				s.refresh(ctx, c, firstListTimeout)
			}(c)
		case stale && !busy:
			go s.refresh(context.Background(), c, listTimeout)
		}
		out = append(out, c)
	}
	wg.Wait()
	return out
}

// refresh mengambil daftar tool satu koneksi dan memperbarui statusnya.
func (s *Service) refresh(ctx context.Context, c *conn, timeout time.Duration) {
	c.mu.Lock()
	if c.refreshing {
		c.mu.Unlock()
		return
	}
	c.refreshing = true
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	tools, err := c.client.ListTools(ctx)
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshing, c.tried = false, true
	switch {
	case err == nil:
		c.state, c.err, c.tools, c.refreshedAt = StateConnected, "", tools, &now
		logger.Info("mcp terhubung", zap.String("user", c.user), zap.String("server", c.name), zap.Int("tools", len(tools)))
	case errors.Is(err, clients.ErrMCPNeedsLogin):
		c.state, c.err, c.tools, c.refreshedAt = StateNeedsLogin, "", nil, &now
	default:
		// daftar tool lama tetap dipakai bila gagal sesaat
		c.state, c.err = StateError, err.Error()
		logger.Warn("mcp gagal mengambil daftar tool", zap.String("user", c.user), zap.String("server", c.name), zap.Error(err))
	}
}

// buildIndex menyusun index tool dari koneksi yang tersambung.
func buildIndex(conns []*conn) *index {
	idx := &index{byName: map[string]*toolEntry{}}
	var docs [][]string
	for _, c := range conns {
		c.mu.Lock()
		tools, keywords := c.tools, c.cfg.Keywords
		c.mu.Unlock()
		for _, t := range tools {
			e := &toolEntry{name: toolName(c.name, t.Name), conn: c, tool: t}
			if _, dup := idx.byName[e.name]; dup {
				continue
			}
			e.spec = buildSpec(e.name, c.name, t)
			idx.entries = append(idx.entries, e)
			idx.byName[e.name] = e

			var doc []string
			for range serverWeight {
				doc = append(doc, bm25.Tokenize(c.name)...)
			}
			for range keywordWeight {
				doc = append(doc, bm25.Tokenize(strings.Join(keywords, " "))...)
			}
			for range toolWeight {
				doc = append(doc, bm25.Tokenize(t.Name+" "+t.Title)...)
			}
			doc = append(doc, bm25.Tokenize(t.Description)...)
			docs = append(docs, doc)
		}
	}
	if len(docs) > 0 {
		idx.corpus = bm25.New(docs)
	}
	return idx
}

type scored struct {
	entry *toolEntry
	score float64
}

func (idx *index) score(query string) []scored {
	if idx.corpus == nil {
		return nil
	}
	var hits []scored
	for i, sc := range idx.corpus.Score(bm25.Tokenize(query)) {
		if sc > 0 {
			hits = append(hits, scored{entry: idx.entries[i], score: sc})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	return hits
}

// ---------------------------------------------------------------------------
// dipakai proxy (user dibaca dari ctx)
// ---------------------------------------------------------------------------

// Select memilih tool milik user di ctx yang cocok dengan pesan (maks
// MaxTools, skor minimal MinScore). Pesan yang tidak menyinggung tool apa pun
// tidak membawa tool sama sekali, jadi tidak menambah token.
func (s *Service) Select(ctx context.Context, query string) []llmtool.Spec {
	user := userctx.From(ctx)
	if s.opt.MaxTools <= 0 || user == "" {
		return nil
	}
	var out []llmtool.Spec
	for _, h := range buildIndex(s.ensure(ctx, user)).score(query) {
		if h.score < s.opt.MinScore || len(out) == s.opt.MaxTools {
			break
		}
		out = append(out, llmtool.Spec{Name: h.entry.name, Source: h.entry.conn.name, Definition: h.entry.spec})
	}
	return out
}

// Has melaporkan apakah tool ini milik salah satu server MCP user di ctx.
func (s *Service) Has(ctx context.Context, name string) bool {
	user := userctx.From(ctx)
	return user != "" && buildIndex(s.existing(user)).byName[name] != nil
}

// Call menjalankan tool yang diminta model dengan akun user di ctx. Hasilnya
// selalu teks untuk model; kegagalan juga dilaporkan sebagai teks (IsError)
// supaya model bisa memperbaiki argumen atau menjelaskan ke user.
func (s *Service) Call(ctx context.Context, name, args string) llmtool.Result {
	e := buildIndex(s.existing(userctx.From(ctx))).byName[name]
	if e == nil {
		return llmtool.Result{Text: "tool " + name + " tidak dikenal atau servernya sedang tidak terhubung", IsError: true}
	}

	callCtx, cancel := context.WithTimeout(ctx, s.opt.ToolTimeout)
	defer cancel()

	start := time.Now()
	res, err := e.conn.client.CallTool(callCtx, e.tool.Name, json.RawMessage(strings.TrimSpace(args)))
	if err != nil {
		if errors.Is(err, clients.ErrMCPNeedsLogin) {
			e.conn.mu.Lock()
			e.conn.state = StateNeedsLogin
			e.conn.mu.Unlock()
			return llmtool.Result{Text: "akses ke " + e.conn.name + " ditolak: user perlu login ulang. Link login: " +
				s.connectURL(e.conn.user, e.conn.name), IsError: true}
		}
		logger.Warn("tool mcp gagal", zap.String("tool", name), zap.Duration("latency", time.Since(start)), zap.Error(err))
		return llmtool.Result{Text: "gagal memanggil tool: " + err.Error(), IsError: true}
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
	return llmtool.Result{Text: text, IsError: res.IsError}
}

// ---------------------------------------------------------------------------
// API (per user)
// ---------------------------------------------------------------------------

// Statuses mengembalikan status semua server yang terlihat oleh user.
func (s *Service) Statuses(ctx context.Context, user string) []ServerStatus {
	conns := map[string]*conn{}
	for _, c := range s.ensure(ctx, user) {
		conns[c.name] = c
	}

	defs := s.defs(ctx, user)
	out := make([]ServerStatus, 0, len(defs))
	for _, d := range defs {
		st := ServerStatus{Name: d.Name, URL: d.Cfg.URL, Own: d.Own, State: StateDisabled}
		if c := conns[d.Name]; c != nil {
			c.mu.Lock()
			st.State, st.Error, st.Tools, st.RefreshedAt = c.state, c.err, len(c.tools), c.refreshedAt
			for _, t := range c.tools {
				if t.ReadOnly {
					st.ReadOnlyTools++
				}
			}
			c.mu.Unlock()
			st.ConnectURL = s.connectURL(user, d.Name)
		}
		if cred, err := s.repo.GetCredential(ctx, user, d.Name); err == nil && cred != nil && cred.AccessTokenEnc != "" {
			st.LoggedIn, st.TokenExpires = true, cred.ExpiresAt
		}
		out = append(out, st)
	}
	return out
}

// AllUsers merangkum server MCP dan status login semua user (untuk admin).
func (s *Service) AllUsers(ctx context.Context) (*AllUsersResult, error) {
	servers, err := s.repo.AllUserServers(ctx)
	if err != nil {
		return nil, err
	}
	conns, err := s.repo.AllConnections(ctx)
	if err != nil {
		return nil, err
	}
	out := &AllUsersResult{Builtin: []string{}, UserServers: servers, Connections: conns}
	for _, d := range s.global {
		out.Builtin = append(out.Builtin, d.Name)
	}
	if out.UserServers == nil {
		out.UserServers = []UserServer{}
	}
	if out.Connections == nil {
		out.Connections = []Connection{}
	}
	return out, nil
}

// Tools menampilkan tool milik user. Dengan q, diurutkan menurut skor dan
// ditandai mana yang akan dikirim ke model untuk pesan q.
func (s *Service) Tools(ctx context.Context, user, q string, limit int) []ToolInfo {
	idx := buildIndex(s.ensure(ctx, user))
	info := func(e *toolEntry) ToolInfo {
		return ToolInfo{Name: e.name, Server: e.conn.name, Tool: e.tool.Name, Title: e.tool.Title,
			Description: clipRunes(e.tool.Description, 300), ReadOnly: e.tool.ReadOnly}
	}

	var out []ToolInfo
	if strings.TrimSpace(q) == "" {
		for _, e := range idx.entries {
			out = append(out, info(e))
		}
	} else {
		selected := map[string]bool{}
		for _, t := range s.Select(userctx.With(ctx, user), q) {
			selected[t.Name] = true
		}
		for _, h := range idx.score(q) {
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

// Refresh mengambil ulang daftar tool satu server untuk user.
func (s *Service) Refresh(ctx context.Context, user, name string) (*ServerStatus, error) {
	d, err := s.enabledDef(ctx, user, name)
	if err != nil {
		return nil, err
	}
	c := s.connFor(user, d)
	c.tokens.invalidate()
	s.refresh(ctx, c, listTimeout)
	return s.status(ctx, user, name), nil
}

// StartLogin menyiapkan login OAuth user ke satu server dan mengembalikan
// alamat halaman login yang dibuka di browser. Klien OAuth didaftarkan
// otomatis (DCR) bila belum.
func (s *Service) StartLogin(ctx context.Context, user, name string) (string, error) {
	d, err := s.enabledDef(ctx, user, name)
	if err != nil {
		return "", err
	}
	if s.box == nil {
		return "", apperror.NewInternalServerError("MCP_TOKEN_KEY belum diisi")
	}
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	info, err := clients.DiscoverMCPAuth(ctx, d.Cfg.URL)
	if err != nil {
		return "", apperror.NewBadRequest(name + ": " + err.Error())
	}

	cred, err := s.repo.GetCredential(ctx, user, name)
	if err != nil {
		return "", err
	}
	if cred == nil {
		cred = &Credential{UserID: user, Server: name}
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
		logger.Info("klien oauth mcp didaftarkan", zap.String("user", user), zap.String("server", name), zap.String("auth_method", reg.AuthMethod))
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
	if err := s.repo.SaveState(ctx, state, oauthState{User: user, Server: name, Verifier: verifier}); err != nil {
		return "", apperror.NewInternalServerError("Gagal menyimpan state login (Redis): " + err.Error())
	}

	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if cred.Resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", cred.Resource))
	}
	return conf.AuthCodeURL(state, opts...), nil
}

// FinishLogin menukar kode dari halaman login menjadi token milik user di
// state, menyimpannya, lalu langsung mengambil daftar tool server itu.
func (s *Service) FinishLogin(ctx context.Context, state, code string) (*ServerStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	st, err := s.repo.TakeState(ctx, state)
	if err != nil {
		return nil, apperror.NewBadRequest(err.Error())
	}
	d, err := s.enabledDef(ctx, st.User, st.Server)
	if err != nil {
		return nil, err
	}
	cred, err := s.repo.GetCredential(ctx, st.User, st.Server)
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
	if err := s.saveToken(ctx, st.User, st.Server, tok, true); err != nil {
		return nil, err
	}

	c := s.connFor(st.User, d)
	c.tokens.invalidate()
	c.client.Close()
	s.refresh(ctx, c, listTimeout)
	return s.status(ctx, st.User, st.Server), nil
}

// Logout menghapus token login user untuk satu server.
func (s *Service) Logout(ctx context.Context, user, name string) (*ServerStatus, error) {
	d, err := s.enabledDef(ctx, user, name)
	if err != nil {
		return nil, err
	}
	if err := s.repo.ClearTokens(ctx, user, name); err != nil {
		return nil, err
	}
	c := s.connFor(user, d)
	c.tokens.invalidate()
	c.client.Close()
	c.mu.Lock()
	c.state, c.err, c.tools = StateNeedsLogin, "", nil
	c.mu.Unlock()
	return s.status(ctx, user, name), nil
}

// AddServer menambah server MCP milik user: URL dicek dulu (harus https dan
// benar server MCP), lalu disimpan di m_mcp_servers dan langsung aktif.
// Mengembalikan status dan catatan tambahan (mis. server butuh API key).
func (s *Service) AddServer(ctx context.Context, user, name, rawURL string, keywords []string) (*ServerStatus, string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !serverNameRe.MatchString(name) {
		return nil, "", errors.New("nama server harus huruf kecil/angka/-/_ (maks 40), mis. notion")
	}
	if _, exists := s.findDef(ctx, user, name); exists {
		return nil, "", fmt.Errorf("server %q sudah ada; hapus dulu bila ingin mengganti URL-nya", name)
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return nil, "", errors.New("URL server MCP tidak valid")
	}
	local := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, "", errors.New("URL server MCP harus https")
	}

	// cek: apakah benar server MCP? (401 = server MCP yang butuh login, sah)
	note := ""
	probe := clients.NewMCPClient(name, u.String(), nil, nil)
	pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	_, perr := probe.ListTools(pctx)
	cancel()
	probe.Close()
	switch {
	case perr == nil:
	case errors.Is(perr, clients.ErrMCPNeedsLogin):
		dctx, dcancel := context.WithTimeout(ctx, 20*time.Second)
		if _, derr := clients.DiscoverMCPAuth(dctx, u.String()); derr != nil {
			note = "server butuh login tetapi tidak menyediakan login OAuth MCP; kemungkinan perlu API key yang harus diisi admin di mcp.json (headers)"
		}
		dcancel()
	default:
		return nil, "", fmt.Errorf("URL itu tidak menjawab sebagai server MCP: %v", perr)
	}

	if len(keywords) == 0 {
		keywords = []string{name}
	}
	kw, _ := json.Marshal(keywords)
	if err := s.repo.CreateUserServer(ctx, &UserServer{UserID: user, Name: name, URL: u.String(), Keywords: string(kw)}); err != nil {
		return nil, "", fmt.Errorf("gagal menyimpan server: %w", err)
	}
	s.invalidateDefs(user)
	logger.Info("server mcp milik user ditambahkan", zap.String("user", user), zap.String("server", name), zap.String("url", u.String()))

	d, _ := s.findDef(ctx, user, name)
	s.refresh(ctx, s.connFor(user, d), listTimeout)
	return s.status(ctx, user, name), note, nil
}

// RemoveServer menghapus server MCP milik user beserta login-nya. Server
// bawaan (mcp.json) tidak bisa dihapus lewat sini.
func (s *Service) RemoveServer(ctx context.Context, user, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, g := range s.global {
		if g.Name == name {
			return fmt.Errorf("server %q adalah server bawaan (mcp.json) dan hanya bisa diubah admin; user cukup logout bila tidak ingin memakainya", name)
		}
	}
	n, err := s.repo.DeleteUserServer(ctx, user, name)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("server %q tidak ada di daftar milikmu", name)
	}
	s.invalidateDefs(user)

	s.mu.Lock()
	if c := s.conns[connKey(user, name)]; c != nil {
		c.client.Close()
		delete(s.conns, connKey(user, name))
	}
	s.mu.Unlock()
	if err := s.repo.DeleteCredential(ctx, user, name); err != nil {
		logger.Warn("hapus kredensial server mcp gagal", zap.String("server", name), zap.Error(err))
	}
	logger.Info("server mcp milik user dihapus", zap.String("user", user), zap.String("server", name))
	return nil
}

func (s *Service) status(ctx context.Context, user, name string) *ServerStatus {
	for _, st := range s.Statuses(ctx, user) {
		if st.Name == name {
			return &st
		}
	}
	return nil
}

func (s *Service) enabledDef(ctx context.Context, user, name string) (serverDef, error) {
	d, ok := s.findDef(ctx, user, name)
	if !ok {
		return d, apperror.NewNotFound("Server MCP " + name + " tidak ada")
	}
	if !d.Cfg.IsEnabled() {
		return d, apperror.NewBadRequest("Server MCP " + name + " dimatikan (enabled: false)")
	}
	return d, nil
}

func (s *Service) redirectURL() string {
	return strings.TrimRight(s.opt.BaseURL, "/") + "/api/mcp/oauth/callback"
}

// connectURL membuat link login yang bisa dibuka langsung di browser. Browser
// tidak bisa mengirim header X-User-Id, jadi user_id ikut di link dan
// ditandatangani (HMAC) supaya tidak bisa diganti ke user lain.
func (s *Service) connectURL(user, name string) string {
	base := strings.TrimRight(s.opt.BaseURL, "/") + "/api/mcp/servers/" + url.PathEscape(name) + "/connect"
	if len(s.opt.LinkKey) == 0 {
		return base
	}
	exp := strconv.FormatInt(time.Now().Add(linkTTL).Unix(), 10)
	q := url.Values{"u": {user}, "exp": {exp}, "sig": {s.linkSig(user, name, exp)}}
	return base + "?" + q.Encode()
}

// VerifyLink memeriksa tanda tangan dan masa berlaku link login.
func (s *Service) VerifyLink(user, name, exp, sig string) bool {
	if len(s.opt.LinkKey) == 0 || user == "" || sig == "" {
		return false
	}
	ts, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > ts {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.linkSig(user, name, exp)))
}

func (s *Service) linkSig(user, name, exp string) string {
	m := hmac.New(sha256.New, s.opt.LinkKey)
	m.Write([]byte(user + "\x00" + name + "\x00" + exp))
	return hex.EncodeToString(m.Sum(nil))
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
func (s *Service) saveToken(ctx context.Context, user, server string, tok *oauth2.Token, fresh bool) error {
	cred, err := s.repo.GetCredential(ctx, user, server)
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

// tokenHandler memenuhi auth.OAuthHandler dari SDK MCP untuk satu (user,
// server): memberi token, memperpanjangnya bila kedaluwarsa, dan menandai
// "butuh login" bila ditolak.
type tokenHandler struct {
	svc          *Service
	user, server string

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
	cred, err := h.svc.repo.GetCredential(ctx, h.user, h.server)
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
		if err := h.svc.saveToken(ctx, h.user, h.server, t, false); err != nil {
			logger.Warn("simpan token mcp yang diperpanjang gagal", zap.String("server", h.server), zap.Error(err))
		} else {
			logger.Info("token mcp diperpanjang", zap.String("user", h.user), zap.String("server", h.server))
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

	return llmtool.Function(name, desc, normalizeSchema(t.InputSchema))
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

// ---------------------------------------------------------------------------
// kelola MCP lewat chat (list_connections, connect_service, add/remove)
// ---------------------------------------------------------------------------

const (
	toolListConnections = "list_connections"
	toolConnectService  = "connect_service"
	toolAddServer       = "add_mcp_server"
	toolRemoveServer    = "remove_mcp_server"
)

// connectIntent menandai pesan yang meminta menyambungkan atau mengecek koneksi.
var connectIntent = []string{"konek", "connect", "sambung", "tersambung", "hubung", "terhubung", "login", "integrasi", "mcp"}

// ConnectTool membuat chat bisa mengelola server MCP milik user di ctx:
// menampilkan status, memberi link login, serta menambah/menghapus server
// milik user (langsung aktif).
type ConnectTool struct {
	svc   *Service
	specs []llmtool.Spec
}

// NewConnectTool membuat tool kelola MCP.
func NewConnectTool(svc *Service) *ConnectTool {
	return &ConnectTool{svc: svc, specs: []llmtool.Spec{
		{Name: toolListConnections, Source: "connect", Definition: llmtool.Function(toolListConnections,
			"Tampilkan semua server MCP milik user ini (bawaan dan yang ia tambahkan) beserta statusnya.",
			map[string]any{"type": "object", "properties": map[string]any{}})},
		{Name: toolConnectService, Source: "connect", Definition: llmtool.Function(toolConnectService,
			"Bantu user menyambungkan satu server MCP (mis. canva) ke akunnya sendiri: memberi link login bila belum tersambung. "+
				"Tampilkan link login sebagai link markdown. Jangan pernah meminta user menempel token/password di chat.",
			map[string]any{"type": "object", "properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "nama server MCP, mis. canva"},
			}, "required": []string{"name"}})},
		{Name: toolAddServer, Source: "connect", Definition: llmtool.Function(toolAddServer,
			"Tambah server MCP baru milik user ini dari URL yang ia berikan (langsung aktif, hanya untuk user ini). "+
				"Pakai hanya bila user memberi URL server MCP. Setelah berhasil, tawarkan link login bila servernya butuh login.",
			map[string]any{"type": "object", "properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "nama pendek server, huruf kecil, mis. notion"},
				"url":      map[string]any{"type": "string", "description": "URL endpoint MCP (https), mis. https://mcp.notion.com/mcp"},
				"keywords": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "kata kunci bahasa user untuk memilih tool server ini, mis. [\"notion\",\"catatan\"]"},
			}, "required": []string{"name", "url"}})},
		{Name: toolRemoveServer, Source: "connect", Definition: llmtool.Function(toolRemoveServer,
			"Hapus server MCP milik user ini beserta login-nya. Konfirmasi dulu ke user sebelum memanggil.",
			map[string]any{"type": "object", "properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "nama server yang dihapus"},
			}, "required": []string{"name"}})},
	}}
}

// Select menawarkan tool bila pesan meminta menyambungkan/mengelola koneksi.
func (t *ConnectTool) Select(ctx context.Context, query string) []llmtool.Spec {
	if userctx.From(ctx) == "" {
		return nil
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		for _, p := range connectIntent {
			if strings.HasPrefix(w, p) {
				return t.specs
			}
		}
	}
	return nil
}

// Has melaporkan apakah tool ini milik kelola MCP.
func (t *ConnectTool) Has(_ context.Context, name string) bool {
	return name == toolListConnections || name == toolConnectService || name == toolAddServer || name == toolRemoveServer
}

// Call menjalankan tool kelola MCP untuk user di ctx.
func (t *ConnectTool) Call(ctx context.Context, name, args string) llmtool.Result {
	user := userctx.From(ctx)
	if user == "" {
		return llmtool.Result{Text: "identitas user (X-User-Id) tidak ada", IsError: true}
	}

	switch name {
	case toolAddServer:
		var a struct {
			Name     string   `json:"name"`
			URL      string   `json:"url"`
			Keywords []string `json:"keywords"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return llmtool.Result{Text: `argumen tidak valid: kirim {"name": "...", "url": "https://..."}`, IsError: true}
		}
		st, note, err := t.svc.AddServer(ctx, user, a.Name, a.URL, a.Keywords)
		if err != nil {
			return llmtool.Result{Text: "gagal menambah server MCP: " + err.Error(), IsError: true}
		}
		text := "Server ditambahkan ke daftar milik user ini dan langsung aktif: " + describeServer(*st)
		if st.State != StateConnected {
			text += "\nLink login: " + st.ConnectURL + "\nMinta user membuka link itu, login, lalu kembali ke chat."
		}
		if note != "" {
			text += "\nCatatan: " + note
		}
		return llmtool.Result{Text: text}
	case toolRemoveServer:
		var a struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal([]byte(args), &a)
		if err := t.svc.RemoveServer(ctx, user, a.Name); err != nil {
			return llmtool.Result{Text: "gagal menghapus server MCP: " + err.Error(), IsError: true}
		}
		return llmtool.Result{Text: "Server " + a.Name + " dihapus dari daftar milik user ini beserta login-nya."}
	}

	servers := t.svc.Statuses(ctx, user)
	if name == toolListConnections {
		var b strings.Builder
		b.WriteString("Server MCP milik user ini:\n")
		for _, st := range servers {
			b.WriteString("- " + describeServer(st) + "\n")
		}
		if len(servers) == 0 {
			b.WriteString("(belum ada)\n")
		}
		b.WriteString("\nServer MCP baru bisa ditambahkan lewat chat dengan memberi URL server MCP-nya.")
		return llmtool.Result{Text: b.String()}
	}

	var a struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(args), &a)
	want := strings.ToLower(strings.TrimSpace(a.Name))
	for _, st := range servers {
		if strings.ToLower(st.Name) != want {
			continue
		}
		switch st.State {
		case StateConnected:
			return llmtool.Result{Text: describeServer(st) + ". Sudah tersambung ke akun user ini, tidak perlu login lagi."}
		case StateDisabled:
			return llmtool.Result{Text: describeServer(st) + ". Server ini dimatikan di mcp.json (enabled: false); admin perlu mengaktifkannya lalu restart."}
		default:
			return llmtool.Result{Text: describeServer(st) + "\n\nLink login (khusus user ini, berlaku 24 jam): " + st.ConnectURL +
				"\nMinta user membuka link itu di browser, login dengan akunnya, lalu setujui akses. Setelah halaman menampilkan " +
				"\"Login berhasil\", user kembali ke chat dan server langsung bisa dipakai. Bila login gagal karena " +
				"server menolak pendaftaran aplikasi, sampaikan bahwa server ini belum bisa disambungkan lewat login biasa."}
		}
	}

	names := make([]string, 0, len(servers))
	for _, st := range servers {
		names = append(names, st.Name)
	}
	return llmtool.Result{Text: fmt.Sprintf("Server MCP %q belum terdaftar untuk user ini. Yang ada: %s. "+
		"Bila user tahu URL server MCP-nya, tambahkan dengan add_mcp_server.", a.Name, strings.Join(names, ", "))}
}

func describeServer(st ServerStatus) string {
	s := fmt.Sprintf("%s: %s", st.Name, st.State)
	if st.Own {
		s += " (ditambahkan user)"
	}
	if st.Tools > 0 {
		s += fmt.Sprintf(", %d tool", st.Tools)
	}
	if st.Error != "" {
		s += ", " + st.Error
	}
	return s
}
