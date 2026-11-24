// Package mcpinstall memasang server MCP dari repo GitHub lewat chat:
//
//  1. analyze: repo diunduh ke open_source_mcp/<owner>__<repo> (dikunci ke satu
//     commit) lalu diperiksa (package.json, pyproject, .env.example, README).
//     Tidak ada kode yang dijalankan di tahap ini.
//  2. submit: AI mengusulkan perintah install/start dan nama variabel rahasia,
//     user mengonfirmasi. Pengajuan user biasa menunggu persetujuan admin dan
//     hanya untuk dirinya. Pengajuan admin langsung disetujui dan berlaku untuk
//     semua user, dengan pilihan:
//     - sharing: satu container berisi data rahasia admin dipakai semua user;
//     - tidak sharing: jadi template, tiap user mengisi data rahasianya sendiri
//     dan mendapat container sendiri (m_mcp_install_instances).
//  3. data rahasia (password, DSN, token) diisi lewat link form bertanda
//     tangan, bukan di chat, lalu disimpan terenkripsi.
//  4. setelah disetujui dan data lengkap, kode dijalankan di container Docker
//     terpisah (tanpa hak istimewa, dibatasi memori/CPU, di network khusus MCP),
//     lalu didaftarkan sebagai server MCP dan langsung bisa dipakai di chat.
package mcpinstall

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/secretbox"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/userctx"
)

const (
	containerPort  = 8000
	containerPref  = "mcpx-"
	markerFile     = ".mdw-source.json"
	envLinkTTL     = 24 * time.Hour
	readmeRunes    = 6000
	maxRepoFiles   = 8000
	maxRepoBytes   = 150 << 20
	maxFileBytes   = 20 << 20
	maxEnvKeys     = 30
	maxEnvValueLen = 4096
	probeEvery     = 5 * time.Second
	reconcileDelay = 15 * time.Second
)

// runnerScript dijalankan di container (image runner berisi git, Node, pnpm,
// Python, uv, dan supergateway). Semua masukan dibaca dari env, bukan disisipkan
// ke teks skrip. Kode di-clone pada commit yang disetujui; install hanya
// diulang bila commit berubah (restart container tidak meng-install ulang).
const runnerScript = `set -e
cd /app
if [ "$(cat .mdw-installed 2>/dev/null)" != "$MCP_SHA" ]; then
  find /app -mindepth 1 -maxdepth 1 -exec rm -rf {} +
  git init -q .
  git remote add origin "$MCP_REPO"
  git fetch -q --depth 1 origin "$MCP_SHA"
  git checkout -q FETCH_HEAD
  if [ -n "$MCP_INSTALL_CMD" ]; then sh -c "$MCP_INSTALL_CMD"; fi
  echo "$MCP_SHA" > .mdw-installed
fi
if [ "$MCP_TRANSPORT" = "http" ]; then exec sh -c "$MCP_START_CMD"; fi
exec supergateway --stdio "$MCP_START_CMD" --outputTransport streamableHttp --port "$MCP_PORT" --streamableHttpPath /mcp
`

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,38}$`)
	repoPart  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	envKeyRe  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	envLineRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Z][A-Z0-9_]*)\s*=`)
	// variabel yang tidak boleh diisi user: dipakai runner atau sistem
	reservedEnv = map[string]bool{"PATH": true, "HOME": true, "HOSTNAME": true, "USER": true, "SHELL": true,
		"LD_PRELOAD": true, "LD_LIBRARY_PATH": true}
)

// Registrar mendaftarkan server hasil install ke MCP; mcp.Service memenuhinya.
type Registrar interface {
	NameAvailable(ctx context.Context, name string) (bool, error)
	RegisterInstalled(ctx context.Context, owner, name, endpoint string, keywords []string, global bool) error
	UnregisterInstalled(ctx context.Context, owner, name string) error
}

// Containers menjalankan container; clients.Docker memenuhinya.
type Containers interface {
	Create(ctx context.Context, s clients.ContainerSpec) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	Inspect(ctx context.Context, id string) (*clients.ContainerState, error)
	Logs(ctx context.Context, id string, tail int) (string, error)
}

// Source mengunduh repo GitHub; clients.GitHub memenuhinya.
type Source interface {
	CommitSHA(ctx context.Context, owner, repo, ref string) (string, error)
	DownloadZip(ctx context.Context, owner, repo, ref string) ([]byte, error)
}

// Options adalah pengaturan Service.
type Options struct {
	ReposPath    string        // folder clone (open_source_mcp)
	Image        string        // image runner
	Network      string        // network Docker khusus MCP
	PublishHost  string        // "" = container dijangkau lewat nama di network; mis. 127.0.0.1 untuk lokal
	MemoryMB     int64         // batas memori per container
	CPUs         float64       // batas CPU per container
	StartTimeout time.Duration // batas waktu clone + install + siap
	BaseURL      string        // alamat publik middleware (link form)
	LinkKey      []byte        // kunci tanda tangan link form
}

// Service mengelola pemasangan MCP dari GitHub.
type Service struct {
	repo   *Repository
	docker Containers // nil = fitur dimatikan (MCP_DOCKER_HOST kosong)
	src    Source
	reg    Registrar
	box    *secretbox.Box // nil = data rahasia tidak bisa disimpan
	opt    Options

	probe func(ctx context.Context, endpoint string) error

	folderMu sync.Mutex
	inflight sync.Map // container yang sedang dijalankan
}

// NewService membuat Service.
func NewService(repo *Repository, docker Containers, src Source, reg Registrar, box *secretbox.Box, opt Options) *Service {
	return &Service{repo: repo, docker: docker, src: src, reg: reg, box: box, opt: opt, probe: probeMCP}
}

// Enabled melaporkan apakah pemasangan bisa dijalankan (Docker dan kunci enkripsi siap).
func (s *Service) Enabled() bool { return s.docker != nil && s.box != nil }

func (s *Service) requireEnabled() error {
	if s.docker == nil {
		return apperror.NewBadRequest("Pasang MCP dari GitHub belum diaktifkan: MCP_DOCKER_HOST kosong")
	}
	if s.box == nil {
		return apperror.NewBadRequest("Pasang MCP dari GitHub butuh MCP_TOKEN_KEY (untuk menyimpan data rahasia terenkripsi)")
	}
	return nil
}

// probeMCP memeriksa apakah server MCP di endpoint sudah menjawab daftar tool.
func probeMCP(ctx context.Context, endpoint string) error {
	c := clients.NewMCPClient("probe", endpoint, nil, nil)
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := c.ListTools(ctx)
	return err
}

// ---------------------------------------------------------------------------
// analyze: unduh dan periksa repo (tanpa menjalankan kode)
// ---------------------------------------------------------------------------

type marker struct {
	RepoURL    string    `json:"repo_url"`
	CommitSHA  string    `json:"commit_sha"`
	AnalyzedAt time.Time `json:"analyzed_at"`
}

// Analyze mengunduh repo pada commit terbaru ke folder clone lalu memeriksanya.
func (s *Service) Analyze(ctx context.Context, rawURL string) (*Analysis, error) {
	owner, repo, ref, err := parseRepoURL(rawURL)
	if err != nil {
		return nil, apperror.NewBadRequest(err.Error())
	}
	sha, err := s.src.CommitSHA(ctx, owner, repo, ref)
	if err != nil {
		return nil, apperror.NewBadRequest(err.Error())
	}
	archive, err := s.src.DownloadZip(ctx, owner, repo, sha)
	if err != nil {
		return nil, apperror.NewBadRequest(err.Error())
	}

	dir := s.folder(owner, repo)
	s.folderMu.Lock()
	defer s.folderMu.Unlock()
	n, err := extractRepo(archive, dir)
	if err != nil {
		return nil, apperror.NewBadRequest("arsip repo tidak bisa dibuka: " + err.Error())
	}
	m, _ := json.Marshal(marker{RepoURL: canonicalURL(owner, repo), CommitSHA: sha, AnalyzedAt: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, markerFile), m, 0o600); err != nil {
		return nil, err
	}

	a := inspectRepo(dir)
	a.RepoURL, a.Owner, a.Repo, a.CommitSHA, a.Folder, a.Files = canonicalURL(owner, repo), owner, repo, sha, dir, n
	logger.Info("repo mcp dianalisis", zap.String("repo", owner+"/"+repo), zap.String("sha", sha), zap.Int("files", n))
	return a, nil
}

func (s *Service) folder(owner, repo string) string {
	return filepath.Join(s.opt.ReposPath, strings.ToLower(owner+"__"+repo))
}

// parseRepoURL membaca https://github.com/owner/repo[.git][/tree/ref].
func parseRepoURL(raw string) (owner, repo, ref string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Host != "github.com" && u.Host != "www.github.com") {
		return "", "", "", errors.New("URL harus repo GitHub publik, mis. https://github.com/bytebase/dbhub")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", "", errors.New("URL harus berbentuk https://github.com/<owner>/<repo>")
	}
	owner, repo = parts[0], strings.TrimSuffix(parts[1], ".git")
	if !repoPart.MatchString(owner) || !repoPart.MatchString(repo) {
		return "", "", "", errors.New("nama owner/repo GitHub tidak valid")
	}
	if len(parts) >= 4 && parts[2] == "tree" {
		ref = strings.Join(parts[3:], "/")
	}
	return owner, repo, ref, nil
}

func canonicalURL(owner, repo string) string {
	return "https://github.com/" + owner + "/" + repo
}

// extractRepo mengekstrak arsip zip GitHub ke dir (isi lama dibuang). Folder
// teratas arsip (<repo>-<sha>/) dibuang; symlink, path keluar folder, dan
// arsip yang terlalu besar ditolak.
func extractRepo(archive []byte, dir string) (int, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return 0, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	root, _ := filepath.Abs(dir)
	var total int64
	files := 0
	for _, f := range zr.File {
		name := f.Name
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" || f.FileInfo().IsDir() {
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		clean := path.Clean("/" + name)
		dst := filepath.Join(root, filepath.FromSlash(clean))
		if !strings.HasPrefix(dst, root+string(os.PathSeparator)) {
			return files, fmt.Errorf("path tidak aman di arsip: %s", f.Name)
		}
		if files++; files > maxRepoFiles {
			return files, fmt.Errorf("repo terlalu banyak file (maks %d)", maxRepoFiles)
		}
		if f.UncompressedSize64 > maxFileBytes {
			continue // file besar (biner, data) tidak perlu untuk pemeriksaan
		}
		if total += int64(f.UncompressedSize64); total > maxRepoBytes {
			return files, errors.New("isi repo terlalu besar (maks 150 MB)")
		}
		if err := writeZipFile(f, dst); err != nil {
			return files, err
		}
	}
	return files, nil
}

func writeZipFile(f *zip.File, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, io.LimitReader(rc, maxFileBytes))
	return err
}

// inspectRepo membaca berkas penanda proyek untuk membantu AI menyusun
// perintah install/start. Hanya membaca, tidak menjalankan apa pun.
func inspectRepo(dir string) *Analysis {
	a := &Analysis{Runtime: "lainnya"}
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }

	if raw, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		a.Runtime = "node"
		var pkg struct {
			Name           string            `json:"name"`
			Scripts        map[string]string `json:"scripts"`
			Bin            json.RawMessage   `json:"bin"`
			Main           string            `json:"main"`
			Engines        map[string]string `json:"engines"`
			PackageManager string            `json:"packageManager"`
		}
		_ = json.Unmarshal(raw, &pkg)
		a.PackageName, a.Scripts, a.NodeEngine = pkg.Name, pkg.Scripts, pkg.Engines["node"]
		var binStr string
		if json.Unmarshal(pkg.Bin, &binStr) == nil && binStr != "" {
			a.Bin = map[string]string{pkg.Name: binStr}
		} else {
			_ = json.Unmarshal(pkg.Bin, &a.Bin)
		}
		if a.Bin == nil && pkg.Main != "" {
			a.Bin = map[string]string{"main": pkg.Main}
		}
		switch {
		case exists("pnpm-lock.yaml"):
			a.PackageManager = "pnpm"
		case exists("yarn.lock"):
			a.PackageManager = "yarn"
		case exists("bun.lockb") || exists("bun.lock"):
			a.PackageManager = "bun"
		case pkg.PackageManager != "":
			a.PackageManager = strings.SplitN(pkg.PackageManager, "@", 2)[0]
		default:
			a.PackageManager = "npm"
		}
	} else if exists("pyproject.toml") || exists("requirements.txt") || exists("setup.py") {
		a.Runtime = "python"
		a.PythonScripts = pyprojectScripts(filepath.Join(dir, "pyproject.toml"))
	}
	a.HasDockerfile = exists("Dockerfile")

	for _, name := range []string{".env.example", ".env.sample", ".env.template", "example.env", "env.example"} {
		if keys := envKeysFrom(filepath.Join(dir, name)); len(keys) > 0 {
			a.EnvExample = keys
			break
		}
	}
	for _, name := range []string{"README.md", "readme.md", "README.MD", "Readme.md", "README"} {
		if raw, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			r := []rune(string(raw))
			if len(r) > readmeRunes {
				r = append(r[:readmeRunes], []rune("\n[... README dipotong]")...)
			}
			a.Readme = string(r)
			break
		}
	}
	return a
}

// pyprojectScripts membaca nama perintah di [project.scripts] (cukup kasar,
// hanya untuk petunjuk).
func pyprojectScripts(file string) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = line == "[project.scripts]" || line == "[tool.poetry.scripts]"
			continue
		}
		if in && strings.Contains(line, "=") && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

func envKeysFrom(file string) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := envLineRe.FindStringSubmatch(sc.Text()); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// submit dan persetujuan
// ---------------------------------------------------------------------------

// Submit mengajukan pemasangan dari repo yang sudah dianalisis. Pengajuan
// admin langsung disetujui (berlaku untuk semua user, admin wajib memilih
// sharing atau tidak); pengajuan user biasa menunggu persetujuan admin dan
// hanya untuk dirinya.
func (s *Service) Submit(ctx context.Context, user string, in SubmitInput) (*View, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	admin := userctx.IsAdmin(ctx)
	if admin && in.Shared == nil {
		return nil, apperror.NewBadRequest("admin wajib memilih shared: true = satu server berisi data rahasia admin untuk semua user; " +
			"false = tiap user mengisi data rahasianya sendiri dan mendapat container sendiri")
	}
	owner, repo, _, err := parseRepoURL(in.RepoURL)
	if err != nil {
		return nil, apperror.NewBadRequest(err.Error())
	}
	raw, err := os.ReadFile(filepath.Join(s.folder(owner, repo), markerFile))
	if err != nil {
		return nil, apperror.NewBadRequest("repo belum dianalisis: jalankan analyze dulu")
	}
	var m marker
	if json.Unmarshal(raw, &m) != nil || len(m.CommitSHA) != 40 {
		return nil, apperror.NewBadRequest("hasil analisis rusak: jalankan analyze lagi")
	}

	name := strings.ToLower(strings.TrimSpace(in.Name))
	if !nameRe.MatchString(name) {
		return nil, apperror.NewBadRequest("nama harus huruf kecil/angka/-/_ (maks 39), mis. dbhub")
	}
	if ok, err := s.reg.NameAvailable(ctx, name); err != nil {
		return nil, err
	} else if !ok {
		return nil, apperror.NewConflict(fmt.Sprintf("nama server MCP %q sudah dipakai; pilih nama lain", name))
	}
	if _, err := s.repo.GetByName(ctx, "", name); err == nil {
		return nil, apperror.NewConflict(fmt.Sprintf("sudah ada pemasangan bernama %q; pilih nama lain", name))
	}

	start := strings.TrimSpace(in.StartCommand)
	if start == "" {
		return nil, apperror.NewBadRequest("start_command wajib diisi")
	}
	transport := strings.ToLower(strings.TrimSpace(in.Transport))
	if transport == "" {
		transport = "stdio"
	}
	if transport != "stdio" && transport != "http" {
		return nil, apperror.NewBadRequest("transport harus stdio atau http")
	}
	httpPath := strings.TrimSpace(in.HTTPPath)
	if httpPath == "" {
		httpPath = "/mcp"
	}
	if !strings.HasPrefix(httpPath, "/") || strings.ContainsAny(httpPath, " ?#") {
		return nil, apperror.NewBadRequest("http_path harus diawali /, mis. /mcp")
	}
	envKeys, err := cleanEnvKeys(in.EnvKeys)
	if err != nil {
		return nil, apperror.NewBadRequest(err.Error())
	}
	kw, _ := json.Marshal(in.Keywords)
	ek, _ := json.Marshal(envKeys)

	row := &Install{
		UserID: user, Name: name, RepoURL: canonicalURL(owner, repo), Owner: owner, Repo: repo,
		CommitSHA: m.CommitSHA, Runtime: inspectRepo(s.folder(owner, repo)).Runtime,
		InstallCommand: strings.TrimSpace(in.InstallCommand), StartCommand: start,
		Transport: transport, HTTPPath: httpPath, Keywords: string(kw), EnvKeys: string(ek),
		Purpose: strings.TrimSpace(in.Purpose), Global: admin, PerUser: admin && !*in.Shared,
		Status: StatusPendingReview,
	}
	if admin {
		now := time.Now()
		row.Status, row.ReviewedBy, row.ReviewedAt = StatusApproved, user, &now
	}
	if err := s.repo.Create(ctx, row); err != nil {
		return nil, err // nama ganda (balapan) dipetakan apperror.Handle menjadi 409
	}
	logger.Info("pemasangan mcp diajukan", zap.String("name", name), zap.String("repo", owner+"/"+repo),
		zap.String("user", user), zap.Bool("admin", admin), zap.Bool("per_user", row.PerUser))

	s.maybeLaunch(row, true)
	return s.view(ctx, row, user), nil
}

func cleanEnvKeys(keys []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, k := range keys {
		k = strings.ToUpper(strings.TrimSpace(k))
		if k == "" || seen[k] {
			continue
		}
		if !envKeyRe.MatchString(k) || reservedEnv[k] || strings.HasPrefix(k, "MCP_") {
			return nil, fmt.Errorf("nama variabel %q tidak boleh dipakai (huruf besar/angka/_, bukan PATH/HOME/MCP_*)", k)
		}
		seen[k] = true
		out = append(out, k)
	}
	if len(out) > maxEnvKeys {
		return nil, fmt.Errorf("variabel rahasia maksimal %d", maxEnvKeys)
	}
	sort.Strings(out)
	return out, nil
}

// Approve menyetujui pengajuan (khusus admin), lalu menjalankannya bila data
// rahasia sudah lengkap.
func (s *Service) Approve(ctx context.Context, admin, id string) (*View, error) {
	if !userctx.IsAdmin(ctx) {
		return nil, apperror.NewForbidden("Hanya admin yang bisa menyetujui pemasangan MCP")
	}
	row, err := s.get(ctx, "", id)
	if err != nil {
		return nil, err
	}
	if row.Status != StatusPendingReview && row.Status != StatusRejected {
		return nil, apperror.NewBadRequest("pemasangan ini tidak sedang menunggu persetujuan (status " + row.Status + ")")
	}
	now := time.Now()
	if err := s.repo.Update(ctx, row.ID, map[string]any{"status": StatusApproved, "reviewed_by": admin, "reviewed_at": &now, "error": ""}); err != nil {
		return nil, err
	}
	row.Status, row.ReviewedBy, row.ReviewedAt, row.Error = StatusApproved, admin, &now, ""
	logger.Info("pemasangan mcp disetujui", zap.String("name", row.Name), zap.String("admin", admin))
	s.maybeLaunch(row, true)
	return s.view(ctx, row, ""), nil
}

// Reject menolak pengajuan (khusus admin).
func (s *Service) Reject(ctx context.Context, admin, id, reason string) (*View, error) {
	if !userctx.IsAdmin(ctx) {
		return nil, apperror.NewForbidden("Hanya admin yang bisa menolak pemasangan MCP")
	}
	row, err := s.get(ctx, "", id)
	if err != nil {
		return nil, err
	}
	if row.Status != StatusPendingReview {
		return nil, apperror.NewBadRequest("hanya pengajuan yang menunggu persetujuan yang bisa ditolak (status " + row.Status + ")")
	}
	now := time.Now()
	msg := strings.TrimSpace(reason)
	if msg == "" {
		msg = "ditolak admin"
	}
	if err := s.repo.Update(ctx, row.ID, map[string]any{"status": StatusRejected, "reviewed_by": admin, "reviewed_at": &now, "error": msg}); err != nil {
		return nil, err
	}
	row.Status, row.Error = StatusRejected, msg
	return s.view(ctx, row, ""), nil
}

// ---------------------------------------------------------------------------
// data rahasia (form)
// ---------------------------------------------------------------------------

// EnvURL membuat link form data rahasia untuk user (berlaku 24 jam).
func (s *Service) EnvURL(id, user string) string {
	if len(s.opt.LinkKey) == 0 {
		return ""
	}
	exp := strconv.FormatInt(time.Now().Add(envLinkTTL).Unix(), 10)
	q := url.Values{"u": {user}, "exp": {exp}, "sig": {s.envSig(id, user, exp)}}
	return strings.TrimRight(s.opt.BaseURL, "/") + "/api/mcp/installs/" + url.PathEscape(id) + "/env?" + q.Encode()
}

// VerifyEnvLink memeriksa tanda tangan dan masa berlaku link form.
func (s *Service) VerifyEnvLink(id, user, exp, sig string) bool {
	if len(s.opt.LinkKey) == 0 || user == "" || sig == "" {
		return false
	}
	ts, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > ts {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(s.envSig(id, user, exp)))
}

func (s *Service) envSig(id, user, exp string) string {
	m := hmac.New(sha256.New, s.opt.LinkKey)
	m.Write([]byte("mcp-env\x00" + id + "\x00" + user + "\x00" + exp))
	return hex.EncodeToString(m.Sum(nil))
}

// EnvForm mengambil data halaman form: nama variabel dan mana yang sudah diisi
// user ini. Template tidak-sharing boleh diisi user mana pun (untuk
// instance-nya sendiri); lainnya hanya oleh peminta.
func (s *Service) EnvForm(ctx context.Context, user, id string) (*View, error) {
	row, err := s.visible(ctx, user, id)
	if err != nil {
		return nil, err
	}
	if !row.PerUser && row.UserID != user {
		return nil, apperror.NewNotFound("Pemasangan MCP tidak ditemukan")
	}
	return s.view(ctx, row, user), nil
}

// SetEnv menyimpan data rahasia dari form. Nilai kosong = tidak diubah. Bila
// sudah disetujui dan lengkap, server dijalankan (ulang) dengan data baru.
func (s *Service) SetEnv(ctx context.Context, user, id string, values map[string]string) (*View, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	row, err := s.visible(ctx, user, id)
	if err != nil {
		return nil, err
	}
	if !row.PerUser && row.UserID != user {
		return nil, apperror.NewNotFound("Pemasangan MCP tidak ditemukan")
	}

	var inst *Instance
	current := row.EnvEnc
	if row.PerUser {
		if inst, err = s.instanceFor(ctx, row, user); err != nil {
			return nil, err
		}
		current = inst.EnvEnc
	}
	enc, changed, err := s.mergeEnv(row, current, values)
	if err != nil {
		return nil, err
	}
	if !changed {
		return s.view(ctx, row, user), nil
	}

	if inst != nil {
		if err := s.repo.UpdateInstance(ctx, inst.ID, map[string]any{"env_enc": enc}); err != nil {
			return nil, err
		}
		inst.EnvEnc = enc
		logger.Info("data rahasia mcp (instance user) diperbarui", zap.String("name", row.Name), zap.String("user", user))
		s.maybeLaunchInstance(row, inst, true)
	} else {
		if err := s.repo.Update(ctx, row.ID, map[string]any{"env_enc": enc}); err != nil {
			return nil, err
		}
		row.EnvEnc = enc
		logger.Info("data rahasia mcp diperbarui", zap.String("name", row.Name))
		s.maybeLaunch(row, true) // data baru baru berlaku pada container baru
	}
	return s.view(ctx, row, user), nil
}

// mergeEnv menggabungkan nilai baru ke data rahasia terenkripsi.
func (s *Service) mergeEnv(row *Install, current string, values map[string]string) (string, bool, error) {
	allowed := map[string]bool{}
	for _, k := range decodeList(row.EnvKeys) {
		allowed[k] = true
	}
	env, err := s.decrypt(current)
	if err != nil {
		return "", false, err
	}
	changed := false
	for k, v := range values {
		if !allowed[k] || v == "" {
			continue
		}
		if len(v) > maxEnvValueLen || strings.ContainsRune(v, 0) {
			return "", false, apperror.NewBadRequest("nilai " + k + " terlalu panjang atau tidak valid")
		}
		env[k], changed = v, true
	}
	if !changed {
		return current, false, nil
	}
	raw, _ := json.Marshal(env)
	enc, err := s.box.Seal(string(raw))
	return enc, err == nil, err
}

// instanceFor mengambil (atau membuat) instance milik user untuk template
// tidak-sharing. Template harus sudah disetujui.
func (s *Service) instanceFor(ctx context.Context, row *Install, user string) (*Instance, error) {
	if row.ReviewedAt == nil || row.Status == StatusRejected || row.Status == StatusPendingReview {
		return nil, apperror.NewBadRequest("pemasangan ini belum disetujui admin")
	}
	inst, err := s.repo.GetInstance(ctx, row.ID, user)
	if err == nil {
		return inst, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	inst = &Instance{InstallID: row.ID, UserID: user, Status: StatusApproved}
	if err := s.repo.CreateInstance(ctx, inst); err != nil {
		// dibuat bersamaan oleh request lain
		if again, gerr := s.repo.GetInstance(ctx, row.ID, user); gerr == nil {
			return again, nil
		}
		return nil, err
	}
	return inst, nil
}

func (s *Service) decrypt(enc string) (map[string]string, error) {
	env := map[string]string{}
	if enc == "" {
		return env, nil
	}
	if s.box == nil {
		return nil, errors.New("MCP_TOKEN_KEY kosong: data rahasia tidak bisa dibuka")
	}
	plain, err := s.box.Open(enc)
	if err != nil {
		return nil, fmt.Errorf("data rahasia tidak bisa dibuka (MCP_TOKEN_KEY berubah?): %w", err)
	}
	_ = json.Unmarshal([]byte(plain), &env)
	return env, nil
}

func (s *Service) missing(row *Install, enc string) []string {
	env, _ := s.decrypt(enc)
	out := []string{}
	for _, k := range decodeList(row.EnvKeys) {
		if env[k] == "" {
			out = append(out, k)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// baca, jalankan, hentikan, hapus
// ---------------------------------------------------------------------------

// List mengambil pemasangan: milik user ditambah pemasangan admin untuk semua
// user. scope "" = semua (admin).
func (s *Service) List(ctx context.Context, scope, viewer, status string) ([]*View, error) {
	rows, err := s.repo.List(ctx, scope, status)
	if err != nil {
		return nil, err
	}
	out := make([]*View, 0, len(rows))
	for i := range rows {
		out = append(out, s.view(ctx, &rows[i], viewer))
	}
	return out, nil
}

// Get mengambil satu pemasangan yang boleh dilihat.
func (s *Service) Get(ctx context.Context, scope, viewer, id string) (*View, error) {
	row, err := s.visible(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, row, viewer), nil
}

// get mengambil pemasangan milik scope ("" = milik siapa pun).
func (s *Service) get(ctx context.Context, scope, id string) (*Install, error) {
	row, err := s.repo.Get(ctx, scope, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NewNotFound("Pemasangan MCP tidak ditemukan")
	}
	return row, err
}

// visible mengambil pemasangan milik scope atau buatan admin untuk semua user.
func (s *Service) visible(ctx context.Context, scope, id string) (*Install, error) {
	row, err := s.repo.GetVisible(ctx, scope, "id", id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NewNotFound("Pemasangan MCP tidak ditemukan")
	}
	return row, err
}

// owns melaporkan apakah scope boleh mengelola pemasangan level atas
// (container bersama / milik peminta).
func owns(scope string, row *Install) bool { return scope == "" || row.UserID == scope }

// Start menjalankan (ulang). Template tidak-sharing: menjalankan instance
// milik user sendiri.
func (s *Service) Start(ctx context.Context, scope, user, id string) (*View, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	row, err := s.visible(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	if row.ReviewedAt == nil || row.Status == StatusRejected || row.Status == StatusPendingReview {
		return nil, apperror.NewBadRequest("pemasangan ini belum disetujui admin")
	}
	if row.PerUser {
		inst, err := s.instanceFor(ctx, row, user)
		if err != nil {
			return nil, err
		}
		if miss := s.missing(row, inst.EnvEnc); len(miss) > 0 {
			return nil, apperror.NewBadRequest("data rahasia milikmu belum lengkap: " + strings.Join(miss, ", ") + "; isi lewat link form")
		}
		if err := s.repo.UpdateInstance(ctx, inst.ID, map[string]any{"status": StatusApproved, "error": ""}); err != nil {
			return nil, err
		}
		inst.Status = StatusApproved
		s.maybeLaunchInstance(row, inst, false)
		return s.view(ctx, row, user), nil
	}
	if !owns(scope, row) {
		return nil, apperror.NewForbidden("Hanya admin atau yang memasang yang bisa menjalankan MCP ini")
	}
	if miss := s.missing(row, row.EnvEnc); len(miss) > 0 {
		return nil, apperror.NewBadRequest("data rahasia belum lengkap: " + strings.Join(miss, ", "))
	}
	if err := s.repo.Update(ctx, row.ID, map[string]any{"status": StatusApproved, "error": ""}); err != nil {
		return nil, err
	}
	row.Status = StatusApproved
	s.maybeLaunch(row, false)
	return s.view(ctx, row, user), nil
}

// Stop menghentikan container dan melepas server MCP-nya (data tetap ada).
// Template tidak-sharing: menghentikan instance milik user sendiri.
func (s *Service) Stop(ctx context.Context, scope, user, id string) (*View, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	row, err := s.visible(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	if row.PerUser {
		inst, err := s.repo.GetInstance(ctx, row.ID, user)
		if err != nil {
			return nil, apperror.NewNotFound("kamu belum menjalankan MCP " + row.Name)
		}
		if err := s.stopTarget(ctx, target{row: row, inst: inst}); err != nil {
			return nil, err
		}
		return s.view(ctx, row, user), nil
	}
	if !owns(scope, row) {
		return nil, apperror.NewForbidden("Hanya admin atau yang memasang yang bisa menghentikan MCP ini")
	}
	if err := s.stopTarget(ctx, target{row: row}); err != nil {
		return nil, err
	}
	row, _ = s.repo.Get(ctx, "", row.ID)
	return s.view(ctx, row, user), nil
}

func (s *Service) stopTarget(ctx context.Context, t target) error {
	if err := s.docker.Stop(ctx, t.container()); err != nil && !errors.Is(err, clients.ErrContainerNotFound) {
		return err
	}
	if err := s.reg.UnregisterInstalled(ctx, t.owner(), t.row.Name); err != nil {
		return err
	}
	status := StatusStopped
	if t.inst == nil && t.row.ReviewedAt == nil {
		status = t.row.Status // pengajuan yang belum disetujui tetap menunggu
	}
	return s.update(ctx, t, map[string]any{"status": status, "endpoint": ""})
}

// Remove menghapus. Admin pada template tidak-sharing: menghapus template
// beserta container SEMUA user. User biasa pada template tidak-sharing:
// menghapus instance miliknya saja.
func (s *Service) Remove(ctx context.Context, scope, user, id string) error {
	row, err := s.visible(ctx, scope, id)
	if err != nil {
		return err
	}
	if row.PerUser && scope != "" {
		inst, err := s.repo.GetInstance(ctx, row.ID, user)
		if err != nil {
			return apperror.NewNotFound("kamu belum memakai MCP " + row.Name)
		}
		if err := s.removeContainer(ctx, target{row: row, inst: inst}); err != nil {
			return err
		}
		return s.repo.DeleteInstance(ctx, inst.ID)
	}
	if !owns(scope, row) {
		return apperror.NewForbidden("Hanya admin atau yang memasang yang bisa menghapus MCP ini")
	}
	if row.PerUser {
		insts, err := s.repo.Instances(ctx, row.ID)
		if err != nil {
			return err
		}
		for i := range insts {
			if err := s.removeContainer(ctx, target{row: row, inst: &insts[i]}); err != nil {
				return err
			}
		}
	} else if err := s.removeContainer(ctx, target{row: row}); err != nil {
		return err
	}
	logger.Info("pemasangan mcp dihapus", zap.String("name", row.Name))
	return s.repo.Delete(ctx, row.ID)
}

func (s *Service) removeContainer(ctx context.Context, t target) error {
	if s.docker != nil {
		if err := s.docker.Remove(ctx, t.container()); err != nil {
			return err
		}
	}
	return s.reg.UnregisterInstalled(ctx, t.owner(), t.row.Name)
}

// Logs mengambil log terakhir container (template tidak-sharing: milik user sendiri).
func (s *Service) Logs(ctx context.Context, scope, user, id string) (string, error) {
	if err := s.requireEnabled(); err != nil {
		return "", err
	}
	row, err := s.visible(ctx, scope, id)
	if err != nil {
		return "", err
	}
	t := target{row: row}
	if row.PerUser {
		inst, err := s.repo.GetInstance(ctx, row.ID, user)
		if err != nil {
			return "", apperror.NewNotFound("kamu belum menjalankan MCP " + row.Name)
		}
		t.inst = inst
	} else if !owns(scope, row) {
		return "", apperror.NewForbidden("Hanya admin atau yang memasang yang bisa melihat log MCP ini")
	}
	out, err := s.docker.Logs(ctx, t.container(), 200)
	if errors.Is(err, clients.ErrContainerNotFound) {
		return "", apperror.NewNotFound("container belum dibuat")
	}
	return out, err
}

func (s *Service) view(ctx context.Context, row *Install, viewer string) *View {
	v := &View{Install: row, Keywords: decodeList(row.Keywords), EnvKeys: decodeList(row.EnvKeys), EnvSet: []string{}}
	enc := row.EnvEnc
	if row.PerUser {
		enc = ""
		if viewer != "" {
			if inst, err := s.repo.GetInstance(ctx, row.ID, viewer); err == nil {
				v.Mine, enc = inst, inst.EnvEnc
			}
		}
	}
	v.EnvMissing = s.missing(row, enc)
	missing := map[string]bool{}
	for _, k := range v.EnvMissing {
		missing[k] = true
	}
	for _, k := range v.EnvKeys {
		if !missing[k] {
			v.EnvSet = append(v.EnvSet, k)
		}
	}
	canFill := row.PerUser || (viewer != "" && viewer == row.UserID)
	if viewer != "" && canFill && len(v.EnvKeys) > 0 {
		v.EnvURL = s.EnvURL(row.ID, viewer)
	}
	return v
}

func decodeList(raw string) []string {
	out := []string{}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// ---------------------------------------------------------------------------
// menjalankan container
// ---------------------------------------------------------------------------

// target adalah sesuatu yang berjalan di satu container: pemasangan (sharing
// atau milik peminta), atau instance milik satu user (template tidak-sharing).
type target struct {
	row  *Install
	inst *Instance
}

func (t target) key() string {
	if t.inst != nil {
		return "i:" + t.inst.ID
	}
	return t.row.ID
}

// owner adalah user yang terdaftar sebagai pemilik server MCP-nya.
func (t target) owner() string {
	if t.inst != nil {
		return t.inst.UserID
	}
	return t.row.UserID
}

func (t target) container() string {
	if t.inst != nil {
		sum := sha256.Sum256([]byte(t.inst.UserID))
		return containerPref + t.row.Name + "-" + hex.EncodeToString(sum[:4])
	}
	return containerPref + t.row.Name
}

func (t target) envEnc() string {
	if t.inst != nil {
		return t.inst.EnvEnc
	}
	return t.row.EnvEnc
}

func (t target) status() string {
	if t.inst != nil {
		return t.inst.Status
	}
	return t.row.Status
}

// global: hanya container bersama buatan admin yang berlaku untuk semua user.
func (t target) global() bool { return t.inst == nil && t.row.Global }

func (s *Service) update(ctx context.Context, t target, fields map[string]any) error {
	if t.inst != nil {
		return s.repo.UpdateInstance(ctx, t.inst.ID, fields)
	}
	return s.repo.Update(ctx, t.row.ID, fields)
}

// maybeLaunch menjalankan pemasangan level atas (sharing / milik peminta) di
// latar belakang bila sudah disetujui dan data rahasianya lengkap. Template
// tidak-sharing tidak punya container sendiri.
func (s *Service) maybeLaunch(row *Install, recreate bool) {
	if row.PerUser {
		return
	}
	s.launchAsync(target{row: row}, recreate)
}

// maybeLaunchInstance menjalankan instance milik satu user.
func (s *Service) maybeLaunchInstance(row *Install, inst *Instance, recreate bool) {
	if row.Status == StatusRejected || row.Status == StatusPendingReview {
		return
	}
	s.launchAsync(target{row: row, inst: inst}, recreate)
}

func (s *Service) launchAsync(t target, recreate bool) {
	if s.docker == nil || t.row.ReviewedAt == nil {
		return
	}
	switch t.status() {
	case StatusApproved, StatusRunning, StatusFailed, StatusStarting:
	default:
		return
	}
	if len(s.missing(t.row, t.envEnc())) > 0 {
		return
	}
	if _, busy := s.inflight.LoadOrStore(t.key(), true); busy {
		return
	}
	go func() {
		defer s.inflight.Delete(t.key())
		defer func() {
			if r := recover(); r != nil {
				logger.Error("menjalankan mcp panic", zap.Any("panic", r))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), s.opt.StartTimeout+2*time.Minute)
		defer cancel()
		if err := s.launch(ctx, t, recreate); err != nil {
			logger.Warn("mcp gagal dijalankan", zap.String("name", t.row.Name), zap.String("container", t.container()), zap.Error(err))
		}
	}()
}

func (s *Service) launch(ctx context.Context, t target, recreate bool) error {
	// baca ulang: data bisa berubah sejak dijadwalkan
	row, err := s.repo.Get(ctx, "", t.row.ID)
	if err != nil {
		return err
	}
	t.row = row
	if t.inst != nil {
		if t.inst, err = s.repo.GetInstance(ctx, row.ID, t.inst.UserID); err != nil {
			return err
		}
	}
	fail := func(msg string) error {
		_ = s.update(context.Background(), t, map[string]any{"status": StatusFailed, "error": clip(msg, 4000)})
		return errors.New(msg)
	}
	env, err := s.decrypt(t.envEnc())
	if err != nil {
		return fail(err.Error())
	}
	if err := s.update(ctx, t, map[string]any{"status": StatusStarting, "error": ""}); err != nil {
		return err
	}
	name := t.container()

	cid := ""
	if !recreate {
		if st, err := s.docker.Inspect(ctx, name); err == nil {
			cid = st.ID
		}
	}
	if cid == "" {
		_ = s.docker.Remove(ctx, name)
		spec := clients.ContainerSpec{
			Name: name, Image: s.opt.Image, Cmd: []string{"sh", "-c", runnerScript},
			Env: containerEnv(row, env),
			Labels: map[string]string{"managed-by": "middleware-skill-ai", "mcp-install-id": row.ID,
				"mcp-name": row.Name, "mcp-owner": t.owner()},
			Network: s.opt.Network, Port: containerPort, PublishHost: s.opt.PublishHost,
			MemoryMB: s.opt.MemoryMB, CPUs: s.opt.CPUs,
		}
		if cid, err = s.docker.Create(ctx, spec); err != nil {
			return fail("container tidak bisa dibuat: " + err.Error())
		}
		_ = s.update(ctx, t, map[string]any{"container_id": cid})
	}
	if err := s.docker.Start(ctx, cid); err != nil {
		return fail("container tidak bisa dijalankan: " + err.Error())
	}

	endpoint, err := s.endpoint(ctx, cid, name, row)
	if err != nil {
		return fail(err.Error())
	}

	// clone + install bisa makan beberapa menit; tunggu sampai server menjawab
	deadline := time.Now().Add(s.opt.StartTimeout)
	for {
		perr := s.probe(ctx, endpoint)
		if perr == nil {
			break
		}
		st, ierr := s.docker.Inspect(ctx, cid)
		if ierr == nil && !st.Running {
			logs, _ := s.docker.Logs(context.Background(), cid, 60)
			_ = s.docker.Stop(context.Background(), cid) // hentikan supaya tidak restart terus-menerus
			return fail(fmt.Sprintf("container berhenti (exit %d). Log terakhir:\n%s", st.ExitCode, logs))
		}
		if time.Now().After(deadline) {
			logs, _ := s.docker.Logs(context.Background(), cid, 60)
			_ = s.docker.Stop(context.Background(), cid)
			return fail(fmt.Sprintf("server MCP tidak siap dalam %s (%v). Log terakhir:\n%s", s.opt.StartTimeout, perr, logs))
		}
		select {
		case <-ctx.Done():
			return fail("dibatalkan: " + ctx.Err().Error())
		case <-time.After(probeEvery):
		}
	}

	if err := s.reg.RegisterInstalled(ctx, t.owner(), row.Name, endpoint, decodeList(row.Keywords), t.global()); err != nil {
		return fail("server berjalan tetapi gagal didaftarkan: " + err.Error())
	}
	logger.Info("mcp dari github berjalan", zap.String("name", row.Name), zap.String("owner", t.owner()), zap.String("endpoint", endpoint))
	return s.update(ctx, t, map[string]any{"status": StatusRunning, "endpoint": endpoint, "error": ""})
}

// containerEnv menyusun env container: masukan runner (MCP_*) dan data
// rahasia user. Env middleware sendiri (password DB, JWT_SECRET, ...) tidak
// pernah ikut.
func containerEnv(row *Install, secrets map[string]string) []string {
	env := []string{
		"MCP_REPO=" + row.RepoURL + ".git",
		"MCP_SHA=" + row.CommitSHA,
		"MCP_INSTALL_CMD=" + row.InstallCommand,
		"MCP_START_CMD=" + row.StartCommand,
		"MCP_TRANSPORT=" + row.Transport,
		"MCP_PORT=" + strconv.Itoa(containerPort),
		"PORT=" + strconv.Itoa(containerPort),
	}
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+secrets[k])
	}
	return env
}

// endpoint menentukan alamat MCP container: lewat nama container di network
// Docker, atau lewat port host (pengembangan lokal).
func (s *Service) endpoint(ctx context.Context, cid, name string, row *Install) (string, error) {
	p := "/mcp"
	if row.Transport == "http" {
		p = row.HTTPPath
	}
	if s.opt.PublishHost == "" {
		return fmt.Sprintf("http://%s:%d%s", name, containerPort, p), nil
	}
	for i := 0; i < 10; i++ {
		st, err := s.docker.Inspect(ctx, cid)
		if err == nil && st.HostPort != "" {
			return fmt.Sprintf("http://%s:%s%s", s.opt.PublishHost, st.HostPort, p), nil
		}
		time.Sleep(time.Second)
	}
	return "", errors.New("port container tidak ditemukan")
}

// Run dipanggil saat start: server yang tadinya berjalan didaftarkan ulang
// (atau dijalankan lagi bila container-nya hilang), termasuk instance per user.
func (s *Service) Run(ctx context.Context) {
	if s.docker == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(reconcileDelay):
	}
	active := []string{StatusRunning, StatusStarting, StatusApproved}
	rows, err := s.repo.ByStatus(ctx, active...)
	if err != nil {
		logger.Warn("baca pemasangan mcp gagal", zap.Error(err))
		return
	}
	for i := range rows {
		s.maybeLaunch(&rows[i], false)
	}
	insts, err := s.repo.InstancesByStatus(ctx, active...)
	if err != nil {
		logger.Warn("baca instance mcp gagal", zap.Error(err))
		return
	}
	for i := range insts {
		if row, err := s.repo.Get(ctx, "", insts[i].InstallID); err == nil {
			s.maybeLaunchInstance(row, &insts[i], false)
		}
	}
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "..." + string(r[len(r)-n:])
}

// ---------------------------------------------------------------------------
// tool chat
// ---------------------------------------------------------------------------

const (
	toolInstall = "install_mcp_from_github"
	toolManage  = "manage_mcp_install"
)

// intent menandai pesan yang menyinggung pemasangan MCP.
var intent = []string{"mcp", "github", "install", "instal", "pasang", "setujui", "approve"}

// Tool membuat chat bisa memasang MCP dari GitHub dan mengelola pemasangannya.
type Tool struct {
	svc   *Service
	specs []llmtool.Spec
}

// NewTool membuat tool chat pemasangan MCP.
func NewTool(svc *Service) *Tool {
	return &Tool{svc: svc, specs: []llmtool.Spec{
		{Name: toolInstall, Source: "mcpinstall", Definition: llmtool.Function(toolInstall,
			"Pasang server MCP dari repo GitHub supaya berjalan dan tersambung ke middleware. Dua langkah: "+
				"(1) action=analyze dengan repo_url: repo diunduh dan diperiksa tanpa dijalankan; "+
				"(2) setelah menjelaskan rencana (nama, perintah install & start, nama variabel rahasia) dan user MENGONFIRMASI, "+
				"action=submit. Jangan pernah meminta password/token/DSN di chat: user mengisinya lewat link form yang diberikan.",
			map[string]any{"type": "object", "properties": map[string]any{
				"action":          map[string]any{"type": "string", "enum": []string{"analyze", "submit"}},
				"repo_url":        map[string]any{"type": "string", "description": "https://github.com/<owner>/<repo>"},
				"name":            map[string]any{"type": "string", "description": "submit: nama server, huruf kecil, mis. dbhub"},
				"install_command": map[string]any{"type": "string", "description": "submit: perintah install/build di folder repo, mis. pnpm install && pnpm build"},
				"start_command":   map[string]any{"type": "string", "description": "submit: perintah menjalankan server MCP. transport stdio: perintah server stdio (mis. node dist/index.js --transport stdio). transport http: server harus mendengarkan 0.0.0.0:8000"},
				"transport":       map[string]any{"type": "string", "enum": []string{"stdio", "http"}, "description": "submit: bawaan stdio (lebih aman dipakai)"},
				"http_path":       map[string]any{"type": "string", "description": "submit, transport http: path endpoint, bawaan /mcp"},
				"env_keys":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "submit: NAMA variabel rahasia yang dibutuhkan (mis. DSN), tanpa nilainya"},
				"keywords":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "submit: kata kunci bahasa user untuk memilih tool server ini"},
				"purpose":         map[string]any{"type": "string", "description": "submit: untuk apa server ini (dibaca admin saat menyetujui)"},
				"shared": map[string]any{"type": "boolean", "description": "submit, WAJIB bila yang meminta admin dan HARUS ditanyakan ke admin dulu: " +
					"true = sharing, satu server berisi data rahasia admin dipakai semua user (mis. database perusahaan); " +
					"false = tidak sharing, tiap user mengisi data rahasianya sendiri dan mendapat server sendiri. Abaikan untuk user biasa."},
			}, "required": []string{"action", "repo_url"}})},
		{Name: toolManage, Source: "mcpinstall", Definition: llmtool.Function(toolManage,
			"Kelola MCP yang dipasang dari GitHub: list (status, termasuk MCP dari admin yang tersedia), env_link (link form data rahasia; "+
				"untuk MCP tidak-sharing dari admin, ini cara user mulai memakainya), start, stop, remove. "+
				"Admin juga: approve / reject pengajuan user. Konfirmasi dulu ke user sebelum stop/remove.",
			map[string]any{"type": "object", "properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"list", "env_link", "start", "stop", "remove", "approve", "reject"}},
				"name":   map[string]any{"type": "string", "description": "nama server (wajib kecuali list)"},
				"reason": map[string]any{"type": "string", "description": "reject: alasan penolakan"},
			}, "required": []string{"action"}})},
	}}
}

// Select menawarkan tool bila pesan menyinggung pemasangan MCP/GitHub. Tool
// tetap ditawarkan walau fitur mati: tanpa tool ini model tidak tahu fitur
// ada dan cenderung mengarang cara lain (ngrok, deploy sendiri, ...); dengan
// tool, model menerima penjelasan jujur bahwa fiturnya belum diaktifkan.
func (t *Tool) Select(ctx context.Context, query string) []llmtool.Spec {
	if userctx.From(ctx) == "" {
		return nil
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		for _, p := range intent {
			if strings.HasPrefix(w, p) {
				return t.specs
			}
		}
	}
	return nil
}

// Has melaporkan apakah tool ini milik pemasangan MCP.
func (t *Tool) Has(_ context.Context, name string) bool {
	return name == toolInstall || name == toolManage
}

// Call menjalankan tool untuk user di ctx.
func (t *Tool) Call(ctx context.Context, name, args string) llmtool.Result {
	user := userctx.From(ctx)
	if user == "" {
		return llmtool.Result{Text: "identitas user tidak ada", IsError: true}
	}
	if !t.svc.Enabled() {
		return llmtool.Result{IsError: true, Text: "Fitur pasang MCP dari GitHub BELUM DIAKTIFKAN di server middleware ini " +
			"(Docker belum tersambung: MCP_DOCKER_HOST kosong, atau MCP_TOKEN_KEY kosong). Sampaikan ke user dengan jujur bahwa " +
			"middleware sebenarnya bisa memasang dan menjalankan MCP dari GitHub di container Docker, tetapi admin server perlu " +
			"mengaktifkannya dulu (lihat docs/13-pasang-mcp-dari-github.md bagian Menyalakan). Jangan menyarankan cara lain " +
			"(ngrok, deploy sendiri, docker run di laptop user) kecuali user memintanya."}
	}
	if name == toolInstall {
		return t.install(ctx, user, args)
	}
	return t.manage(ctx, user, args)
}

func (t *Tool) install(ctx context.Context, user, args string) llmtool.Result {
	var a struct {
		Action string `json:"action"`
		SubmitInput
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return llmtool.Result{Text: "argumen tidak valid: " + err.Error(), IsError: true}
	}
	if a.Action == "analyze" {
		an, err := t.svc.Analyze(ctx, a.RepoURL)
		if err != nil {
			return llmtool.Result{Text: "gagal menganalisis repo: " + err.Error(), IsError: true}
		}
		return llmtool.Result{Text: describeAnalysis(an, userctx.IsAdmin(ctx))}
	}
	v, err := t.svc.Submit(ctx, user, a.SubmitInput)
	if err != nil {
		return llmtool.Result{Text: "gagal mengajukan pemasangan: " + err.Error(), IsError: true}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Pemasangan %q tercatat (status %s).\n", v.Name, v.Status)
	switch {
	case v.Status == StatusPendingReview:
		b.WriteString("Menunggu persetujuan admin sebelum dijalankan; hanya untuk user ini. Sampaikan ke user.\n")
	case v.PerUser:
		b.WriteString("Tidak sharing: berlaku untuk semua user, tetapi tiap user (termasuk admin) mengisi data rahasianya sendiri " +
			"dan mendapat server sendiri. User lain memulainya dengan meminta link form (manage_mcp_install action=env_link).\n")
	case v.Global:
		b.WriteString("Sharing: setelah berjalan, berlaku untuk SEMUA user dengan data rahasia admin.\n")
	}
	if len(v.EnvMissing) > 0 && v.EnvURL != "" {
		fmt.Fprintf(&b, "Data rahasia yang perlu diisi: %s.\nLink form (khusus user ini, berlaku 24 jam, tampilkan sebagai link markdown): %s\n"+
			"Minta user mengisi di link itu, JANGAN di chat.\n", strings.Join(v.EnvMissing, ", "), v.EnvURL)
	}
	if v.Status == StatusApproved && len(v.EnvMissing) == 0 && !v.PerUser {
		b.WriteString("Server sedang di-clone, di-install, dan dijalankan di container (bisa beberapa menit). " +
			"User bisa menanyakan statusnya nanti (manage_mcp_install action=list).\n")
	}
	return llmtool.Result{Text: b.String()}
}

func (t *Tool) manage(ctx context.Context, user, args string) llmtool.Result {
	var a struct {
		Action string `json:"action"`
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return llmtool.Result{Text: "argumen tidak valid: " + err.Error(), IsError: true}
	}
	scope := userctx.Scope(ctx)
	if a.Action == "list" {
		rows, err := t.svc.List(ctx, scope, user, "")
		if err != nil {
			return llmtool.Result{Text: err.Error(), IsError: true}
		}
		if len(rows) == 0 {
			return llmtool.Result{Text: "Belum ada MCP yang dipasang dari GitHub."}
		}
		var b strings.Builder
		for _, v := range rows {
			b.WriteString("- " + describeInstall(v) + "\n")
		}
		return llmtool.Result{Text: b.String()}
	}

	// approve/reject mencari di semua pengajuan (khusus admin); lainnya yang
	// boleh dilihat user (miliknya + MCP dari admin)
	lookup := scope
	if a.Action == "approve" || a.Action == "reject" {
		lookup = ""
	}
	row, err := t.svc.repo.GetVisible(ctx, lookup, "name", strings.ToLower(strings.TrimSpace(a.Name)))
	if err != nil {
		return llmtool.Result{Text: fmt.Sprintf("pemasangan %q tidak ditemukan", a.Name), IsError: true}
	}
	var v *View
	switch a.Action {
	case "env_link":
		if !row.PerUser && row.UserID != user {
			return llmtool.Result{Text: "MCP ini memakai data rahasia milik yang memasang (sharing); user tidak perlu mengisi apa pun", IsError: true}
		}
		v = t.svc.view(ctx, row, user)
		if v.EnvURL == "" {
			if row.PerUser {
				if v, err = t.svc.Start(ctx, scope, user, row.ID); err == nil {
					return llmtool.Result{Text: "MCP ini tidak butuh data rahasia; server milik user ini sedang dijalankan."}
				}
				return llmtool.Result{Text: "gagal: " + err.Error(), IsError: true}
			}
			return llmtool.Result{Text: "pemasangan ini tidak butuh data rahasia"}
		}
		return llmtool.Result{Text: "Link form data rahasia (khusus user ini, tampilkan sebagai link markdown, berlaku 24 jam): " + v.EnvURL +
			"\nSetelah diisi, server milik user langsung dijalankan."}
	case "approve":
		v, err = t.svc.Approve(ctx, user, row.ID)
	case "reject":
		v, err = t.svc.Reject(ctx, user, row.ID, a.Reason)
	case "start":
		v, err = t.svc.Start(ctx, scope, user, row.ID)
	case "stop":
		v, err = t.svc.Stop(ctx, scope, user, row.ID)
	case "remove":
		if err = t.svc.Remove(ctx, scope, user, row.ID); err == nil {
			if row.PerUser && scope != "" {
				return llmtool.Result{Text: "Server MCP " + row.Name + " milik user ini dihapus (data rahasia dan container-nya)."}
			}
			return llmtool.Result{Text: "MCP " + row.Name + " dihapus (container, pendaftaran server, dan datanya)."}
		}
	default:
		return llmtool.Result{Text: "action tidak dikenal", IsError: true}
	}
	if err != nil {
		return llmtool.Result{Text: "gagal: " + err.Error(), IsError: true}
	}
	return llmtool.Result{Text: describeInstall(v)}
}

func describeAnalysis(a *Analysis, admin bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Repo %s/%s dianalisis (commit %s, %d file, disimpan di %s). Belum ada kode yang dijalankan.\n",
		a.Owner, a.Repo, a.CommitSHA[:12], a.Files, a.Folder)
	fmt.Fprintf(&b, "Runtime: %s", a.Runtime)
	if a.PackageManager != "" {
		fmt.Fprintf(&b, " (%s)", a.PackageManager)
	}
	if a.NodeEngine != "" {
		fmt.Fprintf(&b, ", node %s", a.NodeEngine)
	}
	b.WriteString("\n")
	if a.PackageName != "" {
		fmt.Fprintf(&b, "Package: %s\n", a.PackageName)
	}
	if len(a.Scripts) > 0 {
		keys := make([]string, 0, len(a.Scripts))
		for k := range a.Scripts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("Scripts:\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", k, a.Scripts[k])
		}
	}
	if len(a.Bin) > 0 {
		fmt.Fprintf(&b, "Bin: %v\n", a.Bin)
	}
	if len(a.PythonScripts) > 0 {
		fmt.Fprintf(&b, "Python scripts: %s\n", strings.Join(a.PythonScripts, "; "))
	}
	if len(a.EnvExample) > 0 {
		fmt.Fprintf(&b, ".env.example: %s\n", strings.Join(a.EnvExample, ", "))
	}
	if a.Readme != "" {
		b.WriteString("\nREADME (potongan):\n" + a.Readme + "\n")
	}
	b.WriteString("\nLingkungan container: Debian dengan git, Node 22 (npm, pnpm, yarn lewat corepack), Python 3 + uv, " +
		"folder kerja /app berisi repo, port 8000. Tanpa akses ke file VPS.\n" +
		"Langkah berikutnya: susun name, install_command, start_command (utamakan transport stdio), dan env_keys " +
		"(NAMA saja, mis. DSN; nilai bisa dirujuk di start_command sebagai \"$DSN\"). Jelaskan rencana itu ke user dengan singkat " +
		"dan minta konfirmasi. Setelah user setuju, panggil action=submit.")
	if !admin {
		b.WriteString(" Beri tahu user bahwa pemasangannya perlu disetujui admin dulu dan hanya berlaku untuk dirinya.")
	} else {
		b.WriteString(" Yang meminta admin: server berlaku untuk SEMUA user dan langsung dijalankan tanpa persetujuan. " +
			"WAJIB tanyakan ke admin: sharing (satu server berisi data rahasia admin dipakai semua user) " +
			"atau tidak sharing (tiap user mengisi data rahasianya sendiri dan mendapat server sendiri), lalu isi shared.")
	}
	return b.String()
}

func describeInstall(v *View) string {
	s := fmt.Sprintf("%s: %s (repo %s, commit %.12s", v.Name, v.Status, v.RepoURL, v.CommitSHA)
	switch {
	case v.PerUser:
		s += ", dari admin, tidak sharing: tiap user mengisi data rahasianya sendiri"
	case v.Global:
		s += ", dari admin, sharing untuk semua user"
	}
	s += ")"
	if v.Mine != nil {
		s += ", server milikmu: " + v.Mine.Status
		if v.Mine.Error != "" {
			s += " (" + clip(v.Mine.Error, 300) + ")"
		}
	} else if v.PerUser {
		s += ", kamu belum memakainya (minta env_link untuk mulai)"
	}
	if len(v.EnvMissing) > 0 && (v.PerUser || !v.Global || v.EnvURL != "") {
		s += ", data rahasia belum diisi: " + strings.Join(v.EnvMissing, ", ")
	}
	if v.Error != "" {
		s += "\n  error: " + clip(v.Error, 800)
	}
	return s
}
