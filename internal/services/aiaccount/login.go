package aiaccount

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/clients/loginbrowser"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/cookies"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

const (
	// loginTTL: layar login dibuang bila tidak diselesaikan selama ini.
	loginTTL = 15 * time.Minute
	// jendela login dicek sesering ini; begitu cookie sesi muncul, akun
	// disimpan otomatis setelah jeda singkat (redirect selesai, cookie lengkap)
	loginPoll   = 2 * time.Second
	loginSettle = 4 * time.Second
	// batas waktu menyimpan akun (termasuk cek sesi ke provider)
	loginSaveTimeout = time.Minute
)

// Status login lewat browser.
const (
	LoginWaiting = "waiting" // menunggu user login di layar
	LoginSaving  = "saving"  // login terdeteksi, akun sedang disimpan dan dicek
	LoginSaved   = "saved"   // akun tersimpan, jendela login ditutup
	LoginFailed  = "failed"  // gagal menyimpan; lihat Error
)

// LoginView adalah jawaban POST /api/ai/login dan GET /api/ai/login/:id.
type LoginView struct {
	ID string `json:"id"`
	// VNCPath: alamat WebSocket layar browser (noVNC), sudah berisi tiket.
	VNCPath   string    `json:"vnc_path"`
	ExpiresAt time.Time `json:"expires_at"`
	State     string    `json:"state"`
	Account   *Account  `json:"account,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// StartLoginInput adalah body POST /api/ai/login: ProviderID untuk akun baru,
// atau AccountID untuk login ulang akun yang sudah ada.
type StartLoginInput struct {
	ProviderID string `json:"provider_id"`
	AccountID  string `json:"account_id"`
	// Name (opsional) untuk akun baru; kosong = email akun, atau "<provider> N".
	Name string `json:"name" binding:"max=100"`
}

// FinishLoginInput adalah body POST /api/ai/login/:id/finish: menyimpan
// sekarang tanpa menunggu deteksi otomatis.
type FinishLoginInput struct {
	Name string `json:"name" binding:"max=100"`
}

type loginSession struct {
	id, ticket, contextID string
	userID, role          string // yang membuka login; hanya dia yang boleh melihat/menyelesaikannya
	providerID, accountID string
	name                  string
	expires               time.Time

	// dijaga logins.mu
	state   string
	account *Account
	err     string
}

// logins menyimpan login yang sedang berjalan. Layar browser hanya satu,
// jadi hanya satu login sekaligus: login baru dari user yang sama
// menggantikan yang lama, user lain harus menunggu. Login yang sudah selesai
// disimpan sebentar supaya FE bisa membaca hasilnya.
type logins struct {
	browser loginbrowser.Browser
	mu      sync.Mutex
	current *loginSession
	done    map[string]*loginSession
}

// LoginAvailable melaporkan apakah login lewat browser bisa dipakai.
func (s *Service) LoginAvailable(ctx context.Context) bool {
	return s.Enabled() && s.logins.browser.Available(ctx)
}

// StartLogin membuka halaman login provider di browser login. User login
// lewat layar noVNC di FE; begitu login berhasil, akun disimpan otomatis dan
// jendelanya ditutup (FE memantau lewat LoginStatus).
func (s *Service) StartLogin(ctx context.Context, in StartLoginInput) (*LoginView, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	user := userctx.From(ctx)
	if user == "" {
		return nil, apperror.NewUnauthorized("identitas user tidak ada")
	}
	providerID := in.ProviderID
	if in.AccountID != "" {
		acc, err := s.owned(ctx, in.AccountID)
		if err != nil {
			return nil, err
		}
		providerID = acc.ProviderID
	}
	if providerID == "" {
		return nil, apperror.NewBadRequest("pilih provider")
	}
	p, _, err := s.provider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	if p.Info.Auth != AuthBrowser {
		return nil, apperror.NewBadRequest(p.Name + " tidak memakai login browser: tempel API key")
	}
	if !p.Enabled && in.AccountID == "" {
		return nil, apperror.NewBadRequest("provider " + p.Name + " sedang dimatikan admin")
	}
	loginURL := p.LoginURL
	if loginURL == "" {
		loginURL = p.Info.LoginURL
	}
	s.logins.mu.Lock()
	cur := s.logins.current
	s.logins.mu.Unlock()
	if cur != nil && cur.userID != user && time.Now().Before(cur.expires) {
		return nil, apperror.NewConflict("browser login sedang dipakai user lain; coba lagi beberapa menit lagi")
	}
	s.cancelCurrentLogin(ctx)

	contextID, err := s.logins.browser.StartLogin(ctx, loginURL)
	if err != nil {
		if errors.Is(err, loginbrowser.ErrBrowserDown) {
			return nil, apperror.NewBadRequest(err.Error())
		}
		return nil, err
	}
	ls := &loginSession{id: randomHex(12), ticket: randomHex(24), contextID: contextID,
		userID: user, role: userctx.Role(ctx), providerID: p.ID, accountID: in.AccountID,
		name: strings.TrimSpace(in.Name), expires: time.Now().Add(loginTTL), state: LoginWaiting}
	s.logins.mu.Lock()
	s.logins.current = ls
	s.logins.mu.Unlock()
	time.AfterFunc(loginTTL, func() { s.expireLogin(ls.id) })
	go s.watchLogin(ls, p)

	return s.view(ls), nil
}

// watchLogin memantau jendela login sampai cookie sesi provider muncul, lalu
// menyimpan akunnya. Berhenti bila login dibatalkan, diganti, atau kedaluwarsa.
func (s *Service) watchLogin(ls *loginSession, p *Provider) {
	t := time.NewTicker(loginPoll)
	defer t.Stop()
	for range t.C {
		s.logins.mu.Lock()
		active := s.logins.current == ls && ls.state == LoginWaiting
		s.logins.mu.Unlock()
		if !active || time.Now().After(ls.expires) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		got, err := s.logins.browser.Cookies(ctx, ls.contextID, p.Info.CookieDomains)
		cancel()
		if err != nil || !cookies.HasAny(got, p.Info.SessionCookies) {
			continue // belum login (atau DevTools sesaat tidak menjawab)
		}
		time.Sleep(loginSettle)
		_, _ = s.saveLogin(ls, "")
		return
	}
}

// LoginStatus mengembalikan keadaan login: menunggu, sedang menyimpan,
// tersimpan (beserta akunnya), atau gagal.
func (s *Service) LoginStatus(ctx context.Context, loginID string) (*LoginView, error) {
	s.logins.mu.Lock()
	ls := s.logins.current
	if ls == nil || ls.id != loginID {
		ls = s.logins.done[loginID]
	}
	s.logins.mu.Unlock()
	if ls == nil || ls.userID != userctx.From(ctx) {
		return nil, apperror.NewNotFound("sesi login tidak ditemukan atau kedaluwarsa: mulai login lagi")
	}
	return s.view(ls), nil
}

func (s *Service) view(ls *loginSession) *LoginView {
	s.logins.mu.Lock()
	defer s.logins.mu.Unlock()
	return &LoginView{ID: ls.id, VNCPath: "/api/ai/login/vnc?ticket=" + ls.ticket, ExpiresAt: ls.expires,
		State: ls.state, Account: ls.account, Error: ls.err}
}

// FinishLogin menyimpan sesi sekarang juga, tanpa menunggu deteksi otomatis.
func (s *Service) FinishLogin(ctx context.Context, loginID string, in FinishLoginInput) (*Account, error) {
	ls, err := s.loginByID(ctx, loginID)
	if err != nil {
		return nil, err
	}
	return s.saveLogin(ls, strings.TrimSpace(in.Name))
}

// saveLogin mengambil cookie dari jendela login, menyimpannya sebagai akun
// baru (atau cookie baru akun yang login ulang), lalu menutup jendelanya.
// Hanya satu penyimpanan per login (deteksi otomatis atau tombol Selesai).
func (s *Service) saveLogin(ls *loginSession, name string) (*Account, error) {
	s.logins.mu.Lock()
	if ls.state != LoginWaiting {
		s.logins.mu.Unlock()
		return nil, apperror.NewConflict("login ini sedang/sudah disimpan")
	}
	ls.state = LoginSaving
	s.logins.mu.Unlock()

	// identitas pembuka login, karena penyimpanan otomatis berjalan di luar request
	ctx := userctx.WithRole(userctx.With(context.Background(), ls.userID), ls.role)
	ctx, cancel := context.WithTimeout(ctx, loginSaveTimeout)
	defer cancel()

	acc, err := s.storeLogin(ctx, ls, name)
	if errors.Is(err, errNotLoggedIn) {
		s.logins.mu.Lock()
		ls.state = LoginWaiting // belum login: tetap menunggu
		s.logins.mu.Unlock()
		return nil, apperror.NewBadRequest(err.Error())
	}

	s.logins.mu.Lock()
	if err != nil {
		ls.state, ls.err = LoginFailed, err.Error()
	} else {
		ls.state, ls.account = LoginSaved, acc
	}
	if s.logins.done == nil {
		s.logins.done = map[string]*loginSession{}
	}
	for id, old := range s.logins.done {
		if time.Now().After(old.expires) {
			delete(s.logins.done, id)
		}
	}
	s.logins.done[ls.id] = ls
	s.logins.mu.Unlock()
	s.endLogin(ctx, ls)

	if err != nil {
		logger.Warn("simpan login akun AI gagal", zap.Error(err))
		return nil, err
	}
	logger.Info("login akun AI lewat browser selesai", zap.String("account", acc.Name), zap.String("status", acc.Status))
	return acc, nil
}

var errNotLoggedIn = errors.New("belum login: selesaikan login di layar sampai halaman provider terbuka")

func (s *Service) storeLogin(ctx context.Context, ls *loginSession, name string) (*Account, error) {
	p, _, err := s.provider(ctx, ls.providerID)
	if err != nil {
		return nil, err
	}
	got, err := s.logins.browser.Cookies(ctx, ls.contextID, p.Info.CookieDomains)
	if err != nil {
		return nil, err
	}
	if !cookies.HasAny(got, p.Info.SessionCookies) {
		return nil, errNotLoggedIn
	}
	raw, _ := json.Marshal(got)
	c := string(raw)
	if ls.accountID != "" {
		return s.UpdateAccount(ctx, ls.accountID, UpdateAccountInput{Cookies: &c})
	}

	if name == "" {
		name = ls.name
	}
	named := name != ""
	if !named {
		name = p.Name + " 1"
	}
	acc, err := s.CreateAccount(ctx, CreateAccountInput{ProviderID: p.ID, Name: s.uniqueName(ctx, p.ID, name), Cookies: c})
	if err != nil || named {
		return acc, err
	}
	// tanpa nama dari user: pakai email akun bila provider menyebutkannya
	if e, ok := s.cachedSession(acc.ID).(emailer); ok && e.Email() != "" {
		n := s.uniqueName(ctx, p.ID, e.Email())
		if renamed, err := s.UpdateAccount(ctx, acc.ID, UpdateAccountInput{Name: &n}); err == nil {
			acc = renamed
		}
	}
	return acc, nil
}

// uniqueName mengembalikan nama yang belum dipakai akun user ini di provider
// ini: "nama", lalu "nama (2)", ... ("<provider> 1" menjadi "<provider> 2", ...).
func (s *Service) uniqueName(ctx context.Context, providerID, name string) string {
	accs, err := s.repo.Accounts(ctx, userctx.From(ctx))
	if err != nil {
		return name
	}
	taken := map[string]bool{}
	for _, a := range accs {
		if a.ProviderID == providerID {
			taken[a.Name] = true
		}
	}
	if !taken[name] {
		return name
	}
	base, numbered := strings.CutSuffix(name, " 1")
	for i := 2; ; i++ {
		n := name + " (" + strconv.Itoa(i) + ")"
		if numbered {
			n = base + " " + strconv.Itoa(i)
		}
		if !taken[n] {
			return n
		}
	}
}

func (s *Service) cachedSession(id string) Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id].sess
}

// CancelLogin menutup jendela login tanpa menyimpan apa pun. Login yang sudah
// terdeteksi dan sedang disimpan dibiarkan selesai.
func (s *Service) CancelLogin(ctx context.Context, loginID string) error {
	ls, err := s.loginByID(ctx, loginID)
	if err != nil {
		return nil // sudah selesai, tersimpan, atau kedaluwarsa
	}
	s.logins.mu.Lock()
	saving := ls.state == LoginSaving
	s.logins.mu.Unlock()
	if !saving {
		s.endLogin(ctx, ls)
	}
	return nil
}

// VNCAddr memeriksa tiket layar login dan mengembalikan alamat websockify.
func (s *Service) VNCAddr(ctx context.Context, ticket string) (string, bool) {
	s.logins.mu.Lock()
	ls := s.logins.current
	s.logins.mu.Unlock()
	if ls == nil || time.Now().After(ls.expires) || subtle.ConstantTimeCompare([]byte(ls.ticket), []byte(ticket)) != 1 {
		return "", false
	}
	return s.logins.browser.VNCAddr(ctx), true
}

func (s *Service) loginByID(ctx context.Context, id string) (*loginSession, error) {
	s.logins.mu.Lock()
	defer s.logins.mu.Unlock()
	ls := s.logins.current
	if ls == nil || ls.id != id || ls.userID != userctx.From(ctx) || time.Now().After(ls.expires) {
		return nil, apperror.NewNotFound("sesi login tidak ditemukan atau kedaluwarsa: mulai login lagi")
	}
	return ls, nil
}

func (s *Service) endLogin(ctx context.Context, ls *loginSession) {
	s.logins.mu.Lock()
	if s.logins.current == ls {
		s.logins.current = nil
	}
	s.logins.mu.Unlock()
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.logins.browser.Dispose(dctx, ls.contextID); err != nil {
		logger.Warn("tutup jendela login gagal", zap.Error(err))
	}
}

func (s *Service) cancelCurrentLogin(ctx context.Context) {
	s.logins.mu.Lock()
	ls := s.logins.current
	s.logins.mu.Unlock()
	if ls != nil {
		s.endLogin(ctx, ls)
	}
}

func (s *Service) expireLogin(id string) {
	if ls, err := s.loginByIDAny(id); err == nil {
		s.endLogin(context.Background(), ls)
	}
}

// loginByIDAny seperti loginByID tetapi tanpa memeriksa kedaluwarsa.
func (s *Service) loginByIDAny(id string) (*loginSession, error) {
	s.logins.mu.Lock()
	defer s.logins.mu.Unlock()
	if s.logins.current == nil || s.logins.current.id != id {
		return nil, errors.New("tidak ada")
	}
	return s.logins.current, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
