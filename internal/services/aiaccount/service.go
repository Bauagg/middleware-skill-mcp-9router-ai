// Package aiaccount mengelola akun AI milik user (Gemini, ChatGPT, API
// kompatibel OpenAI, ...) dan memakainya untuk membuat gambar (lewat tool
// generate_image), video (tool generate_video), dan menjawab chat saat 9Router
// user tidak bisa dipakai, dengan kuota akun itu.
// Setiap layanan AI punya Driver; akun dipakai bergantian sesuai prioritas
// provider lalu akun, dan akun yang limitnya habis dilewati sementara.
package aiaccount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/Bauagg/middleware-skill-ai/internal/clients"
	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/cookies"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/secretbox"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

// ImageModel adalah nama "model" akun AI di daftar model gambar.
const ImageModel = "akun-ai"

// VideoPath adalah folder penyimpanan video (dibuat otomatis).
const VideoPath = "videos"

const (
	kindText  = "text"
	kindImage = "image"
	kindVideo = "video"

	// sesi diperbarui sesering ini supaya tidak kedaluwarsa
	refreshInterval = 10 * time.Minute
	// lama akun dilewati setelah limit chat / gambar / video habis (limit video harian)
	chatCooldown  = time.Hour
	imageCooldown = time.Hour
	videoCooldown = 12 * time.Hour
	// jeda akun yang sedang dibatasi provider
	blockedCooldown = 15 * time.Minute
	// batas waktu satu jawaban chat, satu gambar (generate + unduh), dan satu
	// video (Veo bisa diam beberapa menit)
	chatTimeout  = 3 * time.Minute
	imageTimeout = 3 * time.Minute
	videoTimeout = 10 * time.Minute
	// batas waktu cek akun dan perbarui sesi
	checkTimeout = 30 * time.Second
)

// Service mengelola provider, akun, dan sesinya.
type Service struct {
	repo    *Repository
	box     *secretbox.Box // nil = fitur mati (MCP_TOKEN_KEY kosong)
	baseURL string         // alamat publik middleware, untuk link video

	drivers map[string]Driver
	order   []string // urutan driver di form

	mu       sync.Mutex
	sessions map[string]cached // id akun -> sesi aktif

	videoBusy sync.Map // user -> sedang membuat video: satu per user, setiap percobaan memakai limit harian
	videoSpec json.RawMessage

	logins logins // login akun lewat browser di server (layar noVNC di FE)
}

type cached struct {
	providerID string
	sess       Session
}

// NewService membuat Service dengan driver bawaan. box nil mematikan fitur
// (rahasia akun tidak bisa disimpan terenkripsi). images dipakai driver API
// kompatibel OpenAI.
func NewService(repo *Repository, box *secretbox.Box, baseURL string, images *clients.ImageClient) *Service {
	s := &Service{repo: repo, box: box, baseURL: baseURL, drivers: map[string]Driver{},
		sessions: map[string]cached{}, videoSpec: videoToolSpec()}
	s.register(geminiDriver{})
	s.register(chatgptDriver{})
	s.register(openaiDriver{images: images, http: &http.Client{}})
	return s
}

func (s *Service) register(d Driver) {
	k := d.Info().Key
	s.drivers[k] = d
	s.order = append(s.order, k)
}

// Enabled melaporkan apakah fitur aktif.
func (s *Service) Enabled() bool { return s.box != nil }

func (s *Service) requireEnabled() error {
	if !s.Enabled() {
		return apperror.NewBadRequest("fitur akun AI belum aktif: isi MCP_TOKEN_KEY di .env (openssl rand -hex 32)")
	}
	return nil
}

// ---------------------------------------------------------------------------
// akun (milik user; admin melihat dan mengelola semua)
// ---------------------------------------------------------------------------

// Accounts mengambil akun milik user (admin: semua user).
func (s *Service) Accounts(ctx context.Context) ([]Account, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	return s.repo.Accounts(ctx, userctx.Scope(ctx))
}

// owned mengambil akun yang boleh dikelola user ini (miliknya; admin: semua).
func (s *Service) owned(ctx context.Context, id string) (*Account, error) {
	return s.repo.Account(ctx, userctx.Scope(ctx), id)
}

// secretFrom membaca rahasia akun dari input sesuai cara login provider.
func secretFrom(info DriverInfo, rawCookies, apiKey string) (map[string]string, error) {
	if info.Auth == AuthAPIKey {
		key := strings.TrimSpace(apiKey)
		if key == "" {
			return nil, apperror.NewBadRequest("API key wajib diisi")
		}
		return map[string]string{secretAPIKey: key}, nil
	}
	c, err := cookies.Parse(rawCookies)
	if err != nil {
		return nil, apperror.NewBadRequest(err.Error())
	}
	if !cookies.HasAny(c, info.SessionCookies) {
		return nil, apperror.NewBadRequest(fmt.Sprintf("cookie sesi %s tidak ditemukan: salin semua cookie %s saat sudah login",
			strings.TrimSuffix(info.SessionCookies[0], "*"), info.Name))
	}
	return c, nil
}

func cookieCount(info DriverInfo, secret map[string]string) int {
	if info.Auth == AuthAPIKey {
		return 0
	}
	return len(secret)
}

// CreateAccount menyimpan akun baru milik user lalu langsung mengeceknya.
func (s *Service) CreateAccount(ctx context.Context, in CreateAccountInput) (*Account, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	owner := userctx.From(ctx)
	if owner == "" {
		return nil, apperror.NewUnauthorized("identitas user tidak ada")
	}
	p, _, err := s.provider(ctx, in.ProviderID)
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, apperror.NewBadRequest("provider " + p.Name + " sedang dimatikan admin")
	}
	secret, err := secretFrom(p.Info, in.Cookies, in.APIKey)
	if err != nil {
		return nil, err
	}
	sealed, err := s.seal(secret)
	if err != nil {
		return nil, err
	}
	row := &Account{
		UserID: owner, ProviderID: p.ID, Name: strings.TrimSpace(in.Name), Secret: sealed,
		CookieCount: cookieCount(p.Info, secret), Enabled: true,
		Priority: s.nextPriority(ctx, owner, p.ID), Status: StatusUnchecked,
	}
	if err := s.repo.CreateAccount(ctx, row); err != nil {
		return nil, err
	}
	logger.Info("akun AI ditambahkan", zap.String("user", owner), zap.String("provider", p.Name), zap.String("name", row.Name))
	return s.CheckAccount(ctx, row.ID)
}

// UpdateAccount mengubah akun. Cookie / API key baru langsung dicek.
func (s *Service) UpdateAccount(ctx context.Context, id string, in UpdateAccountInput) (*Account, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	acc, err := s.owned(ctx, id)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if in.Name != nil {
		if n := strings.TrimSpace(*in.Name); n != "" {
			fields["name"] = n
		}
	}
	if in.Enabled != nil {
		fields["enabled"] = *in.Enabled
	}
	if in.Priority != nil {
		fields["priority"] = *in.Priority
	}
	newSecret := (in.Cookies != nil && strings.TrimSpace(*in.Cookies) != "") || (in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "")
	if newSecret {
		p, _, err := s.provider(ctx, acc.ProviderID)
		if err != nil {
			return nil, err
		}
		secret, err := secretFrom(p.Info, deref(in.Cookies), deref(in.APIKey))
		if err != nil {
			return nil, err
		}
		sealed, err := s.seal(secret)
		if err != nil {
			return nil, err
		}
		fields["secret"], fields["cookie_count"] = sealed, cookieCount(p.Info, secret)
		fields["status"], fields["last_error"] = StatusUnchecked, ""
	}
	if len(fields) > 0 {
		if err := s.repo.UpdateAccount(ctx, id, fields); err != nil {
			return nil, err
		}
	}
	if newSecret {
		s.dropSession(id)
		return s.CheckAccount(ctx, id)
	}
	return s.owned(ctx, id)
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// DeleteAccount menghapus akun beserta sesinya.
func (s *Service) DeleteAccount(ctx context.Context, id string) error {
	if err := s.requireEnabled(); err != nil {
		return err
	}
	if _, err := s.owned(ctx, id); err != nil {
		return err
	}
	if err := s.repo.DeleteAccount(ctx, id); err != nil {
		return err
	}
	s.dropSession(id)
	return nil
}

// ResetCooldown membuat akun langsung dipakai lagi walau tadi limitnya habis.
func (s *Service) ResetCooldown(ctx context.Context, id string) (*Account, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	if _, err := s.owned(ctx, id); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateAccount(ctx, id, map[string]any{
		"chat_cooldown_until": nil, "image_cooldown_until": nil, "video_cooldown_until": nil,
	}); err != nil {
		return nil, err
	}
	return s.owned(ctx, id)
}

// CheckAccount membuka sesi baru dari rahasia tersimpan dan memastikan
// sesinya berlaku. Hasilnya dicatat di status akun.
func (s *Service) CheckAccount(ctx context.Context, id string) (*Account, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	acc, err := s.owned(ctx, id)
	if err != nil {
		return nil, err
	}
	p, d, err := s.provider(ctx, acc.ProviderID)
	if err != nil {
		return nil, err
	}
	s.dropSession(id)
	sess, err := s.session(acc, p, d)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	err = sess.Check(cctx)
	s.persistSecret(acc.ID, sess)
	s.record(acc, err, true)
	return s.owned(ctx, id)
}

// nextPriority menaruh akun baru di urutan terakhir akun user itu di provider ini.
func (s *Service) nextPriority(ctx context.Context, userID, providerID string) int {
	accs, err := s.repo.Accounts(ctx, userID)
	if err != nil {
		return 0
	}
	n := 0
	for _, a := range accs {
		if a.ProviderID == providerID && a.Priority >= n {
			n = a.Priority + 1
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// sesi dan pool akun
// ---------------------------------------------------------------------------

func (s *Service) seal(secret map[string]string) (string, error) {
	raw, err := json.Marshal(secret)
	if err != nil {
		return "", err
	}
	return s.box.Seal(string(raw))
}

// session mengambil sesi akun dari cache, atau membuatnya dari rahasia tersimpan.
func (s *Service) session(acc *Account, p *Provider, d Driver) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.sessions[acc.ID]; ok {
		return c.sess, nil
	}
	plain, err := s.box.Open(acc.Secret)
	if err != nil {
		return nil, err
	}
	var secret map[string]string
	if err := json.Unmarshal([]byte(plain), &secret); err != nil {
		return nil, fmt.Errorf("rahasia akun %s rusak: %w", acc.Name, err)
	}
	sess := d.Open(p, secret)
	s.sessions[acc.ID] = cached{providerID: p.ID, sess: sess}
	return sess, nil
}

func (s *Service) dropSession(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func (s *Service) dropProviderSessions(providerID string) {
	s.mu.Lock()
	for id, c := range s.sessions {
		if c.providerID == providerID {
			delete(s.sessions, id)
		}
	}
	s.mu.Unlock()
}

// persistSecret menyimpan rahasia yang diperbarui provider (mis. cookie hasil
// rotasi), supaya sesi tetap berlaku setelah server restart.
func (s *Service) persistSecret(id string, sess Session) {
	secret, changed := sess.Secret()
	if !changed {
		return
	}
	sealed, err := s.seal(secret)
	if err != nil {
		logger.Warn("enkripsi sesi akun AI gagal", zap.Error(err))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now()
	if err := s.repo.UpdateAccount(ctx, id, map[string]any{
		"secret": sealed, "cookie_count": len(secret), "cookies_updated_at": now,
	}); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		logger.Warn("simpan sesi akun AI gagal", zap.Error(err))
	}
}

// record mencatat hasil pemakaian/pengecekan ke status akun. check=false
// (pemakaian biasa): kesalahan yang bukan milik akun (mis. prompt ditolak)
// hanya dicatat, tidak mengubah status akun.
func (s *Service) record(acc *Account, err error, check bool) {
	now := time.Now()
	fields := map[string]any{"last_checked_at": now}
	switch {
	case err == nil:
		fields["status"], fields["last_error"] = StatusOK, ""
	case errors.Is(err, ErrAuth):
		fields["status"], fields["last_error"] = StatusNeedsLogin, err.Error()
		s.dropSession(acc.ID)
	case errors.Is(err, ErrUsageLimit), errors.Is(err, ErrBlocked):
		fields["last_error"] = err.Error() // sesi tetap berlaku
	case check:
		fields["status"], fields["last_error"] = StatusError, err.Error()
	default:
		fields["last_error"] = err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if uerr := s.repo.UpdateAccount(ctx, acc.ID, fields); uerr != nil && !errors.Is(uerr, gorm.ErrRecordNotFound) {
		logger.Warn("catat status akun AI gagal", zap.Error(uerr))
	}
}

func (s *Service) cool(acc *Account, kind string, d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.repo.UpdateAccount(ctx, acc.ID, map[string]any{kind2col[kind]: time.Now().Add(d)})
}

// kind2col: kolom jeda tiap jenis pemakaian.
var kind2col = map[string]string{
	kindText: "chat_cooldown_until", kindImage: "image_cooldown_until", kindVideo: "video_cooldown_until",
}

func cooldownUntil(a *Account, kind string) *time.Time {
	switch kind {
	case kindText:
		return a.ChatCooldownUntil
	case kindVideo:
		return a.VideoCooldownUntil
	}
	return a.ImageCooldownUntil
}

// candidate adalah satu akun yang bisa dicoba beserta providernya.
type candidate struct {
	acc *Account
	p   *Provider
	d   Driver
}

// usable mengambil akun milik user request ini yang boleh dipakai untuk jenis
// ini, urut prioritas provider lalu akun. Akun user lain tidak pernah dipakai.
func (s *Service) usable(ctx context.Context, kind string) ([]candidate, error) {
	owner := userctx.From(ctx)
	if owner == "" {
		return nil, nil
	}
	provs, err := s.providers(ctx)
	if err != nil {
		return nil, err
	}
	accs, err := s.repo.Accounts(ctx, owner)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []candidate
	for i := range provs {
		p := &provs[i]
		if !p.Enabled || !p.supports(kind) {
			continue
		}
		for j := range accs {
			a := &accs[j]
			until := cooldownUntil(a, kind)
			if a.ProviderID == p.ID && a.Enabled && a.Status != StatusNeedsLogin && (until == nil || now.After(*until)) {
				out = append(out, candidate{acc: a, p: p, d: s.drivers[p.Driver]})
			}
		}
	}
	return out, nil
}

// HasUsable melaporkan apakah ada akun yang bisa dipakai untuk jenis ini.
func (s *Service) HasUsable(ctx context.Context, kind string) bool {
	if !s.Enabled() {
		return false
	}
	c, err := s.usable(ctx, kind)
	return err == nil && len(c) > 0
}

// errNoAccount berarti user belum punya akun yang bisa dipakai untuk jenis ini.
var errNoAccount = errors.New("tidak ada akun AI yang bisa dipakai (belum ditambahkan, dimatikan, perlu login ulang, atau limitnya habis)")

// run menjalankan fn dengan akun yang tersedia, mulai dari prioritas
// tertinggi. Akun lain hanya dicoba bila masalahnya milik akun itu (limit
// habis, sesi tidak berlaku, dibatasi provider, fitur tidak didukung);
// kesalahan lain (mis. prompt ditolak) akan sama di akun lain, jadi tidak
// diulang.
func (s *Service) run(ctx context.Context, kind string, fn func(Session) error) (*candidate, error) {
	cands, err := s.usable(ctx, kind)
	if err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, errNoAccount
	}
	var reasons []string
	for i := range cands {
		c := &cands[i]
		sess, err := s.session(c.acc, c.p, c.d)
		if err == nil {
			err = fn(sess)
			s.persistSecret(c.acc.ID, sess)
		}
		if !errors.Is(err, ErrUnsupported) {
			s.record(c.acc, err, false)
		}
		if err == nil {
			return c, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch {
		case errors.Is(err, ErrUsageLimit):
			d := map[string]time.Duration{kindText: chatCooldown, kindImage: imageCooldown, kindVideo: videoCooldown}[kind]
			s.cool(c.acc, kind, d)
		case errors.Is(err, ErrBlocked):
			s.cool(c.acc, kind, blockedCooldown)
		case errors.Is(err, ErrAuth), errors.Is(err, ErrUnsupported):
		default:
			return nil, err
		}
		logger.Warn("akun AI dilewati", zap.String("provider", c.p.Name), zap.String("account", c.acc.Name),
			zap.String("kind", kind), zap.Error(err))
		reasons = append(reasons, c.p.Name+"/"+c.acc.Name+": "+err.Error())
	}
	return nil, errors.New(strings.Join(reasons, "; "))
}

func (s *Service) addUsage(id, column string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.repo.AddUsage(ctx, id, column); err != nil {
		logger.Warn("catat pemakaian akun AI gagal", zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// chat (cadangan saat 9Router user tidak bisa dipakai)
// ---------------------------------------------------------------------------

// CanChat melaporkan apakah user request ini punya akun yang bisa menjawab chat.
func (s *Service) CanChat(ctx context.Context) bool { return s.HasUsable(ctx, kindText) }

// Chat menjawab percakapan dengan akun AI user yang tersedia. model berisi
// nama provider dan akun yang menjawab.
func (s *Service) Chat(ctx context.Context, messages []clients.ChatMessage) (text, model string, err error) {
	ctx, cancel := context.WithTimeout(ctx, chatTimeout)
	defer cancel()
	c, err := s.run(ctx, kindText, func(sess Session) error {
		var err error
		text, err = sess.Chat(ctx, messages)
		return err
	})
	if err != nil {
		return "", "", err
	}
	s.addUsage(c.acc.ID, "chats")
	return text, c.p.Name + " (akun " + c.acc.Name + ")", nil
}

// ---------------------------------------------------------------------------
// gambar
// ---------------------------------------------------------------------------

// GenerateImage membuat satu gambar dengan akun AI yang tersedia.
func (s *Service) GenerateImage(ctx context.Context, prompt, size string) (*clients.GeneratedImage, error) {
	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	var img *clients.GeneratedImage
	c, err := s.run(ctx, kindImage, func(sess Session) error {
		var err error
		img, err = sess.Image(ctx, prompt, size)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.addUsage(c.acc.ID, "images")
	img.Model = c.p.Name + " (akun " + c.acc.Name + ")"
	return img, nil
}

// ImageSource menggabungkan akun AI user dengan sumber gambar lain (9Router
// user): model "akun-ai" dicoba lebih dulu, sumber lain jadi cadangan.
// Memenuhi imagegen.Generator.
type ImageSource struct {
	svc  *Service
	next imageGenerator
}

type imageGenerator interface {
	Generate(ctx context.Context, model, prompt, size string) (*clients.GeneratedImage, error)
	ImageModels(ctx context.Context) ([]string, error)
}

// NewImageSource membuat ImageSource; next boleh nil (hanya akun AI).
func (s *Service) NewImageSource(next imageGenerator) *ImageSource {
	return &ImageSource{svc: s, next: next}
}

// ImageModels: "akun-ai" lalu model sumber lain. "akun-ai" selalu ada
// (daftar model di-cache pemanggil): akun yang baru ditambahkan langsung
// terpakai, dan user tanpa akun langsung diteruskan ke cadangan.
func (g *ImageSource) ImageModels(ctx context.Context) ([]string, error) {
	models := []string{ImageModel}
	if g.next == nil {
		return models, nil
	}
	more, err := g.next.ImageModels(ctx)
	if err != nil {
		logger.Warn("daftar model gambar cadangan gagal diambil", zap.Error(err))
	}
	return append(models, more...), nil
}

// Generate membuat gambar dengan model yang diminta.
func (g *ImageSource) Generate(ctx context.Context, model, prompt, size string) (*clients.GeneratedImage, error) {
	if model == ImageModel {
		img, err := g.svc.GenerateImage(ctx, prompt, size)
		switch {
		case errors.Is(err, errNoAccount):
			// tanpa jeda: akun yang baru ditambahkan langsung dipakai
			return nil, &clients.ImageError{Status: http.StatusNotFound, Message: err.Error()}
		case err != nil:
			// 429 membuat imagegen melewati akun AI sebentar dan memakai cadangan
			return nil, &clients.ImageError{Status: http.StatusTooManyRequests, Message: err.Error()}
		}
		return img, nil
	}
	if g.next == nil {
		return nil, fmt.Errorf("model gambar %s tidak dikenal", model)
	}
	return g.next.Generate(ctx, model, prompt, size)
}

// ---------------------------------------------------------------------------
// video
// ---------------------------------------------------------------------------

// ErrVideoBusy berarti video lain sedang dibuat.
var ErrVideoBusy = errors.New("video kamu yang lain sedang dibuat; tunggu sampai selesai")

// GenerateVideo membuat satu video dengan akun AI yang tersedia, menyimpannya,
// dan mencatatnya untuk user. Hanya satu video sekaligus per user.
func (s *Service) GenerateVideo(ctx context.Context, userID, prompt string) (*GeneratedVideo, error) {
	if _, busy := s.videoBusy.LoadOrStore(userID, true); busy {
		return nil, ErrVideoBusy
	}
	defer s.videoBusy.Delete(userID)

	ctx, cancel := context.WithTimeout(ctx, videoTimeout)
	defer cancel()
	start := time.Now()
	var data []byte
	c, err := s.run(ctx, kindVideo, func(sess Session) error {
		var err error
		data, err = sess.Video(ctx, prompt)
		return err
	})
	if err != nil {
		return nil, err
	}

	name, err := s.repo.SaveVideo(data)
	if err != nil {
		return nil, fmt.Errorf("video dibuat tetapi gagal disimpan: %w", err)
	}
	row := &GeneratedVideo{
		UserID: userID, URL: strings.TrimRight(s.baseURL, "/") + "/videos/" + name, FileName: name,
		Prompt: prompt, Provider: c.p.Name, Account: c.acc.Name, SizeBytes: len(data), LatencyMs: time.Since(start).Milliseconds(),
	}
	// catatan di database hanya pelengkap: bila gagal, video tetap dikirim ke user
	dbCtx, dbCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer dbCancel()
	if err := s.repo.CreateVideo(dbCtx, row); err != nil {
		logger.Warn("catat video ke database gagal", zap.String("file", name), zap.Error(err))
	}
	s.addUsage(c.acc.ID, "videos")
	logger.Info("video dibuat", zap.String("file", name), zap.Int("bytes", len(data)),
		zap.String("provider", c.p.Name), zap.String("account", c.acc.Name), zap.Duration("latency", time.Since(start)))
	return row, nil
}

// Videos mengambil catatan video yang pernah dibuat.
func (s *Service) Videos(f VideoFilter) ([]GeneratedVideo, int64, error) {
	return s.repo.Videos(f)
}

// Video mengambil satu catatan video.
func (s *Service) Video(userID, id string) (*GeneratedVideo, error) {
	row, err := s.repo.Video(userID, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NewNotFound("video tidak ditemukan")
	}
	return row, err
}

// VideoPath mengembalikan lokasi file video untuk disajikan.
func (s *Service) VideoPath(name string) (string, bool) { return s.repo.VideoPath(name) }

// ---------------------------------------------------------------------------
// perbarui sesi berkala
// ---------------------------------------------------------------------------

// Start menjaga sesi semua akun tetap hidup sampai ctx selesai: sesi
// diperbarui berkala walau akun jarang dipakai atau sedang dimatikan, jadi
// login cukup sekali sampai akunnya dihapus. Putaran pertama berjalan sesaat
// setelah start, supaya sesi langsung segar setelah server restart.
func (s *Service) Start(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		s.refreshAll(ctx)
		t := time.NewTicker(refreshInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.refreshAll(ctx)
			}
		}
	}()
}

func (s *Service) refreshAll(ctx context.Context) {
	provs, err := s.providers(ctx)
	if err != nil {
		logger.Warn("baca provider AI gagal", zap.Error(err))
		return
	}
	byID := map[string]*Provider{}
	for i := range provs {
		byID[provs[i].ID] = &provs[i]
	}
	accs, err := s.repo.Accounts(ctx, "") // semua user
	if err != nil {
		logger.Warn("baca akun AI gagal", zap.Error(err))
		return
	}
	for i := range accs {
		acc := &accs[i]
		p, ok := byID[acc.ProviderID]
		if !ok {
			continue
		}
		// akun yang ditandai perlu login dicoba lagi dengan sesi baru: penolakan
		// tadi bisa saja gangguan sesaat, dan akun pulih sendiri bila sesinya
		// ternyata masih berlaku
		if acc.Status == StatusNeedsLogin {
			s.dropSession(acc.ID)
		}
		sess, err := s.session(acc, p, s.drivers[p.Driver])
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, checkTimeout)
		err = sess.Refresh(cctx)
		cancel()
		s.persistSecret(acc.ID, sess)
		switch {
		case err == nil && acc.Status != StatusOK:
			logger.Info("sesi akun AI pulih", zap.String("provider", p.Name), zap.String("account", acc.Name))
			s.record(acc, nil, true)
		case err == nil:
		case acc.Status == StatusNeedsLogin && errors.Is(err, ErrAuth):
			// masih logout: status tetap, tidak perlu mencatat ulang setiap putaran
		default:
			logger.Warn("perbarui sesi akun AI gagal", zap.String("provider", p.Name), zap.String("account", acc.Name), zap.Error(err))
			s.record(acc, err, true)
		}
	}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
