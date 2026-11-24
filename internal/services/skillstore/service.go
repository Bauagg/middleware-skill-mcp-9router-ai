package skillstore

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
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
	"strings"
	"time"
	"unicode"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/frontmatter"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/llmtool"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/response"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/skillclean"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/tokens"
)

const (
	skillFile = "SKILL.md"
	shortFile = "SHORT.md"

	// MaxZipBytes adalah batas ukuran file zip yang diupload.
	MaxZipBytes = 10 << 20

	// batas isi zip, melindungi dari zip bomb (header zip bisa berbohong,
	// jadi ukuran juga dihitung ulang saat ekstraksi)
	maxEntries    = 500
	maxFileBytes  = 2 << 20
	maxTotalBytes = 20 << 20

	maxNameLen = 64
	maxDescLen = 1024
)

var (
	// nama skill dipakai sebagai nama folder, jadi dibatasi ke slug aman
	nameRe = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)

	// hanya teks instruksi yang dipasang; skill yang butuh script berada di
	// luar lingkup middleware (model di belakang 9Router tidak punya sandbox)
	allowedExt = map[string]bool{
		".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true, ".csv": true,
	}
)

// Service berisi logika bisnis upload dan daftar skill.
type Service struct {
	repo     *Repository
	onChange func()
}

// NewService membuat service skill. onChange (boleh nil) dipanggil setelah
// skill berhasil dipasang, mis. untuk membangun ulang index pencarian.
func NewService(repo *Repository, onChange func()) *Service {
	return &Service{repo: repo, onChange: onChange}
}

// List mengembalikan semua skill yang terpasang, terurut menurut nama.
// Skill yang SKILL.md-nya hilang atau rusak tetap ditampilkan dengan Error terisi.
func (s *Service) List() ([]SkillDetail, error) {
	names, err := s.repo.List()
	if err != nil {
		return nil, internalErr("list skills", err)
	}

	skills := make([]SkillDetail, 0, len(names))
	for _, name := range names {
		st, err := s.repo.Stat(name)
		if err != nil {
			return nil, internalErr("stat skill "+name, err)
		}

		item := SkillDetail{
			Name:      name,
			Files:     st.Files,
			HasShort:  st.HasShort,
			SizeBytes: st.Bytes,
			UpdatedAt: st.ModTime,
		}

		if data, err := s.repo.ReadFile(name, skillFile); err != nil {
			item.Error = "SKILL.md tidak ditemukan"
		} else if meta, err := frontmatter.Parse(data); err != nil {
			item.Error = err.Error()
		} else {
			item.Description = meta.Description
		}

		skills = append(skills, item)
	}
	return skills, nil
}

// truncMarker ditambahkan di akhir skill yang dipotong, supaya model tahu
// isinya tidak lengkap.
const (
	truncMarker       = "\n\n[... dipotong karena batas token]"
	truncMarkerTokens = 10
)

// Render menyiapkan teks skill yang akan disisipkan ke prompt:
//  1. SKILL.md dibersihkan dari bagian khusus Claude (lihat pkg/skillclean);
//  2. bila hasilnya melebihi budget token, diganti SHORT.md (bila ada);
//  3. bila masih melebihi budget, dipotong di batas paragraf.
//
// budget <= 0 berarti tanpa batas. Error-nya berupa error biasa; pemanggil
// (proxy) memperlakukannya sebagai "skill tidak disisipkan".
func (s *Service) Render(dir string, budget int) (*Rendered, error) {
	data, err := s.repo.ReadFile(dir, skillFile)
	if err != nil {
		return nil, fmt.Errorf("baca %s/%s: %w", dir, skillFile, err)
	}

	text, st := skillclean.Clean(frontmatter.Body(data))
	if text == "" {
		return nil, fmt.Errorf("isi skill %s kosong setelah dibersihkan", dir)
	}
	r := &Rendered{Text: text, Variant: VariantFull, RemovedLines: st.Lines, RemovedBlocks: st.Blocks}

	if budget > 0 && tokens.Estimate(r.Text) > budget {
		if raw, err := s.repo.ReadFile(dir, shortFile); err == nil {
			if short, sst := skillclean.Clean(frontmatter.Body(raw)); short != "" {
				r.Text, r.Variant = short, VariantShort
				r.RemovedLines, r.RemovedBlocks = sst.Lines, sst.Blocks
			}
		}

		if tokens.Estimate(r.Text) > budget {
			cut, _ := tokens.Truncate(r.Text, budget-truncMarkerTokens)
			r.Text, r.Variant = cut+truncMarker, VariantTruncated
		}
	}

	r.Tokens = tokens.Estimate(r.Text)
	return r, nil
}

// RenderVariant menyiapkan teks varian tertentu tanpa melihat budget: "full"
// (SKILL.md) atau "short" (SHORT.md), sudah dibersihkan. Dipakai eval untuk
// membandingkan kualitas versi skill.
func (s *Service) RenderVariant(dir, variant string) (*Rendered, error) {
	file := skillFile
	switch variant {
	case VariantFull:
	case VariantShort:
		file = shortFile
	default:
		return nil, fmt.Errorf("varian %q tidak dikenal (pakai full atau short)", variant)
	}

	data, err := s.repo.ReadFile(dir, file)
	if err != nil {
		return nil, fmt.Errorf("baca %s/%s: %w", dir, file, err)
	}

	text, st := skillclean.Clean(frontmatter.Body(data))
	if text == "" {
		return nil, fmt.Errorf("isi %s/%s kosong setelah dibersihkan", dir, file)
	}
	return &Rendered{
		Text:          text,
		Tokens:        tokens.Estimate(text),
		Variant:       variant,
		RemovedLines:  st.Lines,
		RemovedBlocks: st.Blocks,
	}, nil
}

// Block membungkus teks skill dengan format yang selalu sama sebelum
// disisipkan ke system prompt. Format yang stabil membantu provider yang
// mendukung prefix caching menagih bagian ini lebih murah. Dipakai proxy dan eval.
func Block(name, text string) string {
	return `<skill name="` + strings.ReplaceAll(name, `"`, "'") + `">` + "\n" + text + "\n</skill>"
}

// entry adalah satu file di dalam zip dengan path relatif yang sudah bersih.
type entry struct {
	rel string
	f   *zip.File
}

// pendingSkill adalah skill yang sudah lolos validasi dan siap dipasang.
type pendingSkill struct {
	name        string
	description string
	prefix      string // "" bila SKILL.md ada di root zip, selain itu "folder/"
	files       []entry
}

func fieldErr(field, msg string) response.FieldError {
	return response.FieldError{Field: field, Message: msg}
}

// internalErr mencatat penyebab asli ke log tanpa membocorkannya ke client.
func internalErr(msg string, err error) error {
	logger.Error(msg, zap.Error(err))
	return apperror.NewInternalServerError("Gagal memproses skill")
}

// Upload memvalidasi zip lalu memasang skill di dalamnya secara atomik:
// semua file diekstrak ke folder staging dulu, baru dipindah (rename) ke
// folder skill. Bila ada yang gagal, skill yang sudah terpasang dipulihkan.
func (s *Service) Upload(zr *zip.Reader, input UploadInput) (*UploadResult, error) {
	entries, skipped, err := collectEntries(zr)
	if err != nil {
		return nil, err
	}

	roots, err := findRoots(entries)
	if err != nil {
		return nil, err
	}

	grouped, outside := groupByRoot(entries, roots)
	skipped = append(skipped, outside...)

	pending, err := parseSkills(roots, grouped)
	if err != nil {
		return nil, err
	}

	if !input.Overwrite {
		var errs []response.FieldError
		for _, p := range pending {
			if s.repo.Exists(p.name) {
				errs = append(errs, fieldErr("name",
					fmt.Sprintf("skill '%s' sudah ada, kirim overwrite=true untuk menggantinya", p.name)))
			}
		}
		if len(errs) > 0 {
			return nil, apperror.NewConflict("Skill sudah ada", errs...)
		}
	}

	staging, err := s.repo.NewStagingDir()
	if err != nil {
		return nil, internalErr("create staging dir", err)
	}
	defer os.RemoveAll(staging)

	budget := int64(maxTotalBytes)
	for _, p := range pending {
		for _, e := range p.files {
			dst := filepath.Join(staging, p.name, filepath.FromSlash(strings.TrimPrefix(e.rel, p.prefix)))
			if err := extractFile(staging, dst, e, &budget); err != nil {
				return nil, err
			}
		}
	}

	type swapped struct{ name, backup string }
	done := make([]swapped, 0, len(pending))
	for _, p := range pending {
		backup, err := s.repo.Swap(filepath.Join(staging, p.name), p.name, input.Overwrite)
		if err != nil {
			for i := len(done) - 1; i >= 0; i-- {
				s.repo.Rollback(done[i].name, done[i].backup)
			}
			return nil, internalErr("install skill "+p.name, err)
		}
		done = append(done, swapped{p.name, backup})
	}

	result := &UploadResult{Skills: make([]SkillInfo, 0, len(pending)), SkippedFiles: []string{}}
	for i, p := range pending {
		s.repo.Discard(done[i].backup)

		hasShort := false
		for _, e := range p.files {
			if strings.TrimPrefix(e.rel, p.prefix) == shortFile {
				hasShort = true
			}
		}
		result.Skills = append(result.Skills, SkillInfo{
			Name:        p.name,
			Description: p.description,
			Files:       len(p.files),
			HasShort:    hasShort,
			Replaced:    done[i].backup != "",
		})
	}
	sort.Strings(skipped)
	result.SkippedFiles = append(result.SkippedFiles, skipped...)

	// skill baru langsung bisa dipilih tanpa restart; hook tidak boleh
	// membatalkan upload karena file sudah terpasang
	if s.onChange != nil {
		s.onChange()
	}

	logger.Info("skills uploaded", zap.Int("count", len(result.Skills)))
	return result, nil
}

// collectEntries membaca daftar file zip, menolak path berbahaya (zip slip,
// symlink), dan memisahkan file yang tidak didukung ke daftar skipped.
func collectEntries(zr *zip.Reader) ([]entry, []string, error) {
	if len(zr.File) > maxEntries {
		return nil, nil, apperror.NewBadRequest("Isi zip terlalu banyak",
			fieldErr("file", fmt.Sprintf("maksimal %d file di dalam zip", maxEntries)))
	}

	var (
		entries  []entry
		skipped  []string
		declared uint64
		seen     = map[string]bool{}
	)

	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if isJunk(name) {
			continue
		}
		if !safePath(name) {
			return nil, nil, apperror.NewBadRequest("Zip berisi path yang tidak aman",
				fieldErr("file", fmt.Sprintf("path '%s' tidak diizinkan", f.Name)))
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return nil, nil, apperror.NewBadRequest("Zip berisi symlink",
				fieldErr("file", fmt.Sprintf("symlink '%s' tidak diizinkan", f.Name)))
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			continue
		}

		rel := path.Clean(name)
		if seen[rel] {
			return nil, nil, apperror.NewBadRequest("Zip berisi file ganda",
				fieldErr("file", fmt.Sprintf("file '%s' muncul lebih dari sekali", rel)))
		}
		seen[rel] = true

		if !allowedExt[strings.ToLower(path.Ext(rel))] {
			skipped = append(skipped, rel)
			continue
		}

		if f.UncompressedSize64 > maxFileBytes {
			return nil, nil, apperror.NewBadRequest("File di dalam zip terlalu besar",
				fieldErr("file", fmt.Sprintf("'%s' melebihi %d MB", rel, maxFileBytes>>20)))
		}
		declared += f.UncompressedSize64
		if declared > maxTotalBytes {
			return nil, nil, apperror.NewBadRequest("Isi zip terlalu besar",
				fieldErr("file", fmt.Sprintf("total isi zip melebihi %d MB", maxTotalBytes>>20)))
		}

		entries = append(entries, entry{rel: rel, f: f})
	}

	return entries, skipped, nil
}

// isJunk menandai file sampah bawaan OS yang aman diabaikan diam-diam.
func isJunk(name string) bool {
	base := path.Base(name)
	return strings.HasPrefix(name, "__MACOSX/") ||
		strings.Contains(name, "/__MACOSX/") ||
		base == ".DS_Store" || base == "Thumbs.db" || strings.HasPrefix(base, "._")
}

// safePath menolak path absolut, drive Windows, byte NUL, dan segmen "..".
func safePath(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, ":\x00") {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// findRoots mencari lokasi SKILL.md. Dua bentuk zip yang didukung:
//   - SKILL.md di root zip (satu skill)
//   - <folder>/SKILL.md untuk tiap skill (satu atau banyak skill)
func findRoots(entries []entry) ([]string, error) {
	for _, e := range entries {
		if e.rel == skillFile {
			return []string{""}, nil
		}
	}

	var roots []string
	for _, e := range entries {
		parts := strings.SplitN(e.rel, "/", 2)
		if len(parts) == 2 && parts[1] == skillFile {
			roots = append(roots, parts[0]+"/")
		}
	}
	if len(roots) == 0 {
		return nil, apperror.NewBadRequest("SKILL.md tidak ditemukan",
			fieldErr("file", "zip harus berisi SKILL.md di root atau di dalam folder tingkat pertama (nama-skill/SKILL.md)"))
	}

	sort.Strings(roots)
	return roots, nil
}

// groupByRoot membagi file ke tiap skill; file di luar semua folder skill
// dikembalikan sebagai skipped.
func groupByRoot(entries []entry, roots []string) (map[string][]entry, []string) {
	grouped := make(map[string][]entry, len(roots))
	var outside []string

	for _, e := range entries {
		matched := false
		for _, root := range roots {
			if strings.HasPrefix(e.rel, root) {
				grouped[root] = append(grouped[root], e)
				matched = true
				break
			}
		}
		if !matched {
			outside = append(outside, e.rel)
		}
	}
	return grouped, outside
}

// parseSkills membaca frontmatter tiap SKILL.md dan memvalidasi name/description.
func parseSkills(roots []string, grouped map[string][]entry) ([]pendingSkill, error) {
	pending := make([]pendingSkill, 0, len(roots))
	names := map[string]string{}

	for _, root := range roots {
		label := strings.TrimSuffix(root, "/")
		if label == "" {
			label = "(root zip)"
		}

		var skillEntry *entry
		for i := range grouped[root] {
			if grouped[root][i].rel == root+skillFile {
				skillEntry = &grouped[root][i]
				break
			}
		}

		data, err := readAll(skillEntry.f)
		if err != nil {
			return nil, err
		}

		meta, err := frontmatter.Parse(data)
		if err != nil {
			return nil, apperror.NewBadRequest("Frontmatter SKILL.md tidak valid",
				fieldErr("file", fmt.Sprintf("%s: %s", label, err.Error())))
		}
		name, desc := meta.Name, meta.Description

		if len(name) > maxNameLen || !nameRe.MatchString(name) {
			return nil, apperror.NewBadRequest("Nama skill tidak valid",
				fieldErr("name", fmt.Sprintf("%s: name '%s' harus huruf kecil/angka dipisah '-' atau '_' (maks %d karakter)", label, name, maxNameLen)))
		}
		if desc == "" {
			return nil, apperror.NewBadRequest("Deskripsi skill wajib diisi",
				fieldErr("description", fmt.Sprintf("%s: description wajib diisi dan menjelaskan kapan skill dipakai", label)))
		}
		if len(desc) > maxDescLen {
			return nil, apperror.NewBadRequest("Deskripsi skill terlalu panjang",
				fieldErr("description", fmt.Sprintf("%s: description maksimal %d karakter", label, maxDescLen)))
		}
		if prev, dup := names[name]; dup {
			return nil, apperror.NewBadRequest("Nama skill ganda di dalam zip",
				fieldErr("name", fmt.Sprintf("'%s' dipakai oleh %s dan %s", name, prev, label)))
		}
		names[name] = label

		pending = append(pending, pendingSkill{name: name, description: desc, prefix: root, files: grouped[root]})
	}

	sort.Slice(pending, func(i, j int) bool { return pending[i].name < pending[j].name })
	return pending, nil
}

// readAll membaca satu file zip dengan batas ukuran.
func readAll(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, zipReadErr("open "+f.Name, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, maxFileBytes+1))
	if err != nil {
		return nil, zipReadErr("read "+f.Name, err)
	}
	if len(data) > maxFileBytes {
		return nil, apperror.NewBadRequest("File di dalam zip terlalu besar",
			fieldErr("file", fmt.Sprintf("'%s' melebihi %d MB", f.Name, maxFileBytes>>20)))
	}
	return data, nil
}

// extractFile menulis satu file zip ke dst. Ukuran dihitung ulang saat
// menyalin karena ukuran di header zip bisa dipalsukan.
func extractFile(base, dst string, e entry, budget *int64) error {
	// pertahanan berlapis: dst wajib tetap berada di dalam folder staging
	if rel, err := filepath.Rel(base, dst); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return apperror.NewBadRequest("Zip berisi path yang tidak aman",
			fieldErr("file", fmt.Sprintf("path '%s' tidak diizinkan", e.rel)))
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return internalErr("mkdir "+dst, err)
	}

	rc, err := e.f.Open()
	if err != nil {
		return zipReadErr("open "+e.rel, err)
	}
	defer rc.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return internalErr("create "+dst, err)
	}

	limit := int64(maxFileBytes)
	if *budget < limit {
		limit = *budget
	}
	n, copyErr := io.Copy(out, io.LimitReader(rc, limit+1))
	closeErr := out.Close()

	if copyErr != nil {
		return zipReadErr("copy "+e.rel, copyErr)
	}
	if n > limit {
		return apperror.NewBadRequest("Isi zip terlalu besar",
			fieldErr("file", fmt.Sprintf("'%s' melebihi batas ukuran setelah diekstrak", e.rel)))
	}
	if closeErr != nil {
		return internalErr("close "+dst, closeErr)
	}

	*budget -= n
	return nil
}

// zipReadErr memetakan error baca zip: data rusak = salah client (400),
// sisanya masalah server (500).
func zipReadErr(op string, err error) error {
	var corrupt flate.CorruptInputError
	if errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrFormat) || errors.Is(err, zip.ErrAlgorithm) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &corrupt) {
		return apperror.NewBadRequest("File zip rusak", fieldErr("file", "isi zip tidak bisa dibaca"))
	}
	return internalErr(op, err)
}

// ---------------------------------------------------------------------------
// impor skill dari GitHub lewat chat (tool import_skills_from_github)
// ---------------------------------------------------------------------------

// ImportToolName adalah nama tool yang dilihat model.
const ImportToolName = "import_skills_from_github"

var (
	githubURLRe  = regexp.MustCompile(`(?i)github\.com/[\w.-]+/[\w.-]+`)
	githubPartRe = regexp.MustCompile(`^[\w.-]+$`)
)

// RepoDownloader mengunduh arsip repo; clients.GitHub memenuhinya.
type RepoDownloader interface {
	DownloadZip(ctx context.Context, owner, repo, ref string) ([]byte, error)
}

// ImportTool membuat model bisa memasang skill dari repo GitHub: repo
// diunduh, setiap folder yang berisi SKILL.md (di kedalaman berapa pun)
// disusun ulang menjadi zip skill yang rapi (<nama>/SKILL.md), lalu dipasang
// lewat Upload (aturan dan pengamanan yang sama dengan upload zip). User juga
// mendapat link download zip skill yang terpasang.
type ImportTool struct {
	svc     *Service
	gh      RepoDownloader
	baseURL string
	spec    json.RawMessage
}

// NewImportTool membuat tool impor skill.
func NewImportTool(svc *Service, gh RepoDownloader, baseURL string) *ImportTool {
	return &ImportTool{svc: svc, gh: gh, baseURL: strings.TrimRight(baseURL, "/"), spec: llmtool.Function(ImportToolName,
		"Unduh skill dari repo GitHub publik, pasang ke middleware, dan buat zip-nya. Pakai bila user memberi link "+
			"github.com dan meminta skill diambil/dipasang/dijadikan zip. Setiap folder berisi SKILL.md menjadi satu skill. "+
			"Bila skill dengan nama sama sudah ada, tanyakan dulu ke user sebelum memanggil lagi dengan overwrite=true.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":       map[string]any{"type": "string", "description": "link repo GitHub, mis. https://github.com/owner/repo atau .../tree/main/skills"},
				"overwrite": map[string]any{"type": "boolean", "description": "true = ganti skill yang sudah ada dengan nama sama"},
			},
			"required": []string{"url"},
		})}
}

// Select menawarkan tool hanya bila pesan user berisi link GitHub.
func (t *ImportTool) Select(_ context.Context, query string) []llmtool.Spec {
	if !githubURLRe.MatchString(query) {
		return nil
	}
	return []llmtool.Spec{{Name: ImportToolName, Source: "skills", Definition: t.spec}}
}

// Has melaporkan apakah tool ini milik impor skill.
func (t *ImportTool) Has(_ context.Context, name string) bool { return name == ImportToolName }

// Call mengunduh repo, memasang skill-nya, lalu memberi link zip ke model.
func (t *ImportTool) Call(ctx context.Context, _ string, args string) llmtool.Result {
	var a struct {
		URL       string `json:"url"`
		Overwrite bool   `json:"overwrite"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(args)), &a); err != nil || strings.TrimSpace(a.URL) == "" {
		return llmtool.Result{Text: `argumen tidak valid: kirim {"url": "https://github.com/owner/repo"}`, IsError: true}
	}
	owner, repo, ref, sub, err := parseGitHubURL(a.URL)
	if err != nil {
		return llmtool.Result{Text: err.Error(), IsError: true}
	}

	dlCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	archive, err := t.gh.DownloadZip(dlCtx, owner, repo, ref)
	if err != nil {
		return llmtool.Result{Text: "gagal mengunduh repo: " + err.Error(), IsError: true}
	}

	skillZip, found, notes, err := buildSkillZip(archive, repo, sub)
	if err != nil {
		return llmtool.Result{Text: err.Error(), IsError: true}
	}
	zr, err := zip.NewReader(bytes.NewReader(skillZip), int64(len(skillZip)))
	if err != nil {
		return llmtool.Result{Text: "zip skill rusak: " + err.Error(), IsError: true}
	}

	res, err := t.svc.Upload(zr, UploadInput{Overwrite: a.Overwrite})
	if err != nil {
		var ae *apperror.AppError
		if errors.As(err, &ae) {
			msg := ae.Message
			for _, fe := range ae.Errors {
				msg += "; " + fe.Message
			}
			if ae.StatusCode == 409 {
				msg += ". Tanyakan ke user apakah skill yang sudah ada boleh diganti; bila ya, panggil lagi dengan overwrite=true."
			}
			return llmtool.Result{Text: "gagal memasang skill: " + msg, IsError: true}
		}
		return llmtool.Result{Text: "gagal memasang skill: " + err.Error(), IsError: true}
	}

	names := make([]string, 0, len(res.Skills))
	var b strings.Builder
	fmt.Fprintf(&b, "Berhasil memasang %d skill dari github.com/%s/%s (ditemukan %d folder SKILL.md):\n", len(res.Skills), owner, repo, found)
	for _, s := range res.Skills {
		names = append(names, s.Name)
		status := "baru"
		if s.Replaced {
			status = "diganti"
		}
		fmt.Fprintf(&b, "- %s (%s, %d file): %s\n", s.Name, status, s.Files, clipText(s.Description, 140))
	}
	if n := len(res.SkippedFiles); n > 0 {
		fmt.Fprintf(&b, "\n%d file tidak dipasang (script/non-teks, mis. %s). Model tidak bisa menjalankan script, "+
			"jadi skill yang bergantung pada script mungkin tidak bekerja penuh.\n", n, strings.Join(firstN(res.SkippedFiles, 4), ", "))
	}
	for _, n := range notes {
		b.WriteString("\nCatatan: " + n)
	}
	zipURL := t.baseURL + "/api/skills/export?names=" + url.QueryEscape(strings.Join(names, ","))
	b.WriteString("\n\nLink download zip skill yang terpasang: " + zipURL +
		"\nTampilkan ke user sebagai link markdown [Download zip skill](" + zipURL + "). Skill langsung bisa dipakai di chat berikutnya.")
	logger.Info("skill diimpor dari github", zap.String("repo", owner+"/"+repo), zap.Strings("skills", names))
	return llmtool.Result{Text: b.String()}
}

// parseGitHubURL membaca owner, repo, ref, dan subfolder dari link GitHub:
// github.com/owner/repo(.git), .../tree/<ref>/<subfolder...>.
func parseGitHubURL(raw string) (owner, repo, ref, sub string, err error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, perr := url.Parse(raw)
	if perr != nil || !strings.EqualFold(strings.TrimPrefix(u.Host, "www."), "github.com") {
		return "", "", "", "", errors.New("link harus repo publik di github.com, mis. https://github.com/owner/repo")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", "", "", errors.New("link GitHub harus berisi owner dan nama repo")
	}
	owner, repo = parts[0], strings.TrimSuffix(parts[1], ".git")
	if !githubPartRe.MatchString(owner) || !githubPartRe.MatchString(repo) {
		return "", "", "", "", errors.New("owner atau nama repo tidak valid")
	}
	if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "blob") {
		ref = parts[3]
		sub = strings.Join(parts[4:], "/")
		if parts[2] == "blob" { // link ke file: pakai foldernya
			sub = path.Dir(sub)
		}
		if sub == "." {
			sub = ""
		}
	}
	return owner, repo, ref, sub, nil
}

// buildSkillZip menyusun ulang arsip repo menjadi zip skill: setiap folder
// yang berisi SKILL.md (di kedalaman berapa pun, di bawah sub bila diisi)
// menjadi <nama-folder>/... di root zip. Folder skill di dalam folder skill
// lain dianggap bagian dari skill luarnya. File di luar folder skill dibuang.
func buildSkillZip(archive []byte, repo, sub string) (out []byte, found int, notes []string, err error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, 0, nil, errors.New("arsip repo rusak: " + err.Error())
	}

	type file struct {
		rel string
		f   *zip.File
	}
	var files []file
	var dirs []string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		i := strings.Index(f.Name, "/") // buang folder teratas "<repo>-<ref>/"
		if i < 0 {
			continue
		}
		rel := f.Name[i+1:]
		if sub != "" && !strings.HasPrefix(rel, strings.Trim(sub, "/")+"/") {
			continue
		}
		files = append(files, file{rel: rel, f: f})
		if path.Base(rel) == skillFile {
			dirs = append(dirs, path.Dir(rel))
		}
	}
	if len(dirs) == 0 {
		where := "repo"
		if sub != "" {
			where = "folder " + sub
		}
		return nil, 0, nil, fmt.Errorf("tidak ada SKILL.md di %s, jadi tidak ada skill yang bisa dipasang", where)
	}

	// folder terluar dulu; folder skill di dalam folder skill lain dilewati
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) < len(dirs[j]) })
	var roots []string
	for _, d := range dirs {
		nested := false
		for _, r := range roots {
			if r == "." || strings.HasPrefix(d, r+"/") {
				nested = true
				break
			}
		}
		if !nested {
			roots = append(roots, d)
		}
	}

	nameOf := map[string]string{}
	used := map[string]bool{}
	for _, r := range roots {
		n := path.Base(r)
		if r == "." {
			n = repo
		}
		if used[n] {
			notes = append(notes, "folder "+r+" dilewati karena namanya sama dengan skill lain")
			continue
		}
		used[n], nameOf[r] = true, n
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var total int64
	for _, fl := range files {
		root := ""
		for r := range nameOf {
			if (r == "." || strings.HasPrefix(fl.rel, r+"/")) && len(r) >= len(root) {
				root = r
			}
		}
		if root == "" {
			continue
		}
		inner := fl.rel
		if root != "." {
			inner = strings.TrimPrefix(fl.rel, root+"/")
		}
		if fl.f.UncompressedSize64 > maxFileBytes {
			continue // file besar (mis. gambar) tidak dipasang Upload juga
		}
		data, err := readAll(fl.f)
		if err != nil {
			return nil, 0, nil, err
		}
		if total += int64(len(data)); total > maxTotalBytes {
			return nil, 0, nil, errors.New("isi skill di repo terlalu besar (maks 20 MB)")
		}
		w, err := zw.Create(nameOf[root] + "/" + inner)
		if err != nil {
			return nil, 0, nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, 0, nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, 0, nil, err
	}
	if buf.Len() > MaxZipBytes {
		return nil, 0, nil, errors.New("zip skill melebihi 10 MB")
	}
	return buf.Bytes(), len(roots), notes, nil
}

// Export menyusun zip berisi skill yang terpasang (<nama>/...). names kosong
// = semua skill. File yang diawali "." (cache embedding) tidak ikut.
func (s *Service) Export(names []string) ([]byte, error) {
	if len(names) == 0 {
		all, err := s.repo.List()
		if err != nil {
			return nil, internalErr("list skills", err)
		}
		names = all
	}
	for _, n := range names {
		if !nameRe.MatchString(n) || !s.repo.Exists(n) {
			return nil, apperror.NewNotFound("Skill '" + n + "' tidak ditemukan")
		}
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		files, err := s.repo.Files(n)
		if err != nil {
			return nil, internalErr("list files "+n, err)
		}
		for _, f := range files {
			data, err := s.repo.ReadFile(n, filepath.FromSlash(f))
			if err != nil {
				return nil, internalErr("read "+n+"/"+f, err)
			}
			w, err := zw.Create(n + "/" + f)
			if err != nil {
				return nil, err
			}
			if _, err := w.Write(data); err != nil {
				return nil, err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func clipText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "..."
}

func firstN(list []string, n int) []string {
	if len(list) > n {
		return list[:n]
	}
	return list
}

// ---------------------------------------------------------------------------
// buat skill lewat chat (tool create_skill)
// ---------------------------------------------------------------------------

// CreateToolName adalah nama tool yang dilihat model.
const CreateToolName = "create_skill"

// minSkillRunes mencegah skill kosong/asal-asalan.
const minSkillRunes = 300

// createVerbs menandai pesan yang meminta membuat atau mencari skill.
var createVerbs = []string{"buat", "bikin", "create", "tulis", "generate", "rancang", "susun", "cari", "carikan"}

// CreateTool membuat model bisa menulis skill baru sendiri: model menulis isi
// SKILL.md (dan SHORT.md), middleware menyusun frontmatter yang benar,
// memasangnya lewat Upload (aturan yang sama dengan upload zip), lalu memberi
// link download zip.
type CreateTool struct {
	svc     *Service
	baseURL string
	spec    json.RawMessage
}

// NewCreateTool membuat tool pembuat skill.
func NewCreateTool(svc *Service, baseURL string) *CreateTool {
	return &CreateTool{svc: svc, baseURL: strings.TrimRight(baseURL, "/"), spec: llmtool.Function(CreateToolName,
		"Buat dan pasang skill baru yang ditulis sendiri (mis. skill meta-ads, copywriting, riset pasar). "+
			"Pakai bila user meminta dibuatkan skill, atau saat mencari skill tidak menemukan yang cocok dan user setuju dibuatkan. "+
			"Aturan menulis skill: pakai bahasa user; isi berupa panduan kerja yang bisa langsung dipraktikkan dengan bagian "+
			"'# Judul', '## Kapan dipakai', '## Langkah kerja', '## Aturan dan hal yang dihindari', '## Contoh'; "+
			"jangan merujuk script, file lain, atau tool yang tidak ada (model hanya membaca teks); "+
			"panjang isi sekitar 600-2500 kata. Bila skill dengan nama sama sudah ada, tanyakan dulu sebelum overwrite=true.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "nama skill: huruf kecil, angka, dipisah -, mis. meta-ads"},
				"description": map[string]any{"type": "string", "description": "satu-dua kalimat KAPAN skill dipakai (dicocokkan dengan pesan user), maks 1024 karakter"},
				"keywords":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "kata kunci bahasa Indonesia dan Inggris yang sering dipakai user untuk topik ini"},
				"content":     map[string]any{"type": "string", "description": "isi SKILL.md dalam markdown (tanpa frontmatter)"},
				"short":       map[string]any{"type": "string", "description": "ringkasan 200-400 token untuk model kecil (isi SHORT.md), opsional"},
				"overwrite":   map[string]any{"type": "boolean", "description": "true = ganti skill dengan nama sama"},
			},
			"required": []string{"name", "description", "content"},
		})}
}

// Select menawarkan create_skill bila pesan meminta membuat/mencari skill.
func (t *CreateTool) Select(_ context.Context, query string) []llmtool.Spec {
	words := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-'
	})
	hasSkill, hasVerb := false, false
	for _, w := range words {
		if strings.HasPrefix(w, "skill") || strings.HasPrefix(w, "skil") {
			hasSkill = true
		}
		for _, v := range createVerbs {
			if strings.HasPrefix(w, v) || strings.HasPrefix(w, "mem"+v) || strings.HasPrefix(w, "di"+v) {
				hasVerb = true
			}
		}
	}
	if !hasSkill || !hasVerb {
		return nil
	}
	return []llmtool.Spec{{Name: CreateToolName, Source: "skills", Definition: t.spec}}
}

// Has melaporkan apakah tool ini milik pembuat skill.
func (t *CreateTool) Has(_ context.Context, name string) bool { return name == CreateToolName }

// Call menyusun SKILL.md, memasangnya, lalu memberi link zip.
func (t *CreateTool) Call(ctx context.Context, _ string, args string) llmtool.Result {
	var a struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Keywords    []string `json:"keywords"`
		Content     string   `json:"content"`
		Short       string   `json:"short"`
		Overwrite   bool     `json:"overwrite"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(args)), &a); err != nil {
		return llmtool.Result{Text: "argumen tidak valid: " + err.Error(), IsError: true}
	}
	a.Name = strings.ToLower(strings.TrimSpace(a.Name))
	a.Description = strings.Join(strings.Fields(a.Description), " ")
	a.Content = strings.TrimSpace(a.Content)
	switch {
	case !nameRe.MatchString(a.Name) || len(a.Name) > maxNameLen:
		return llmtool.Result{Text: "nama skill harus huruf kecil/angka dipisah - atau _ (maks 64), mis. meta-ads", IsError: true}
	case a.Description == "" || len([]rune(a.Description)) > maxDescLen:
		return llmtool.Result{Text: "description wajib diisi (maks 1024 karakter) dan menjelaskan kapan skill dipakai", IsError: true}
	case len([]rune(a.Content)) < minSkillRunes:
		return llmtool.Result{Text: "isi skill terlalu pendek; tulis panduan kerja yang lengkap (langkah, aturan, contoh)", IsError: true}
	case len(a.Content) > maxFileBytes:
		return llmtool.Result{Text: "isi skill terlalu panjang (maks 2 MB)", IsError: true}
	}
	if strings.HasPrefix(a.Content, "---") { // model kadang ikut menulis frontmatter: dibuang, diganti yang benar
		if i := strings.Index(a.Content[3:], "\n---"); i >= 0 {
			a.Content = strings.TrimSpace(a.Content[3+i+4:])
		}
	}

	var keywords []string
	for _, k := range a.Keywords {
		if k = strings.TrimSpace(k); k != "" {
			keywords = append(keywords, k)
		}
	}
	fm, err := yaml.Marshal(struct {
		Name        string   `yaml:"name"`
		Description string   `yaml:"description"`
		Keywords    []string `yaml:"keywords,omitempty"`
	}{a.Name, a.Description, keywords})
	if err != nil {
		return llmtool.Result{Text: "gagal menyusun frontmatter: " + err.Error(), IsError: true}
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{a.Name + "/" + skillFile: "---\n" + string(fm) + "---\n\n" + a.Content + "\n"}
	if s := strings.TrimSpace(a.Short); s != "" {
		files[a.Name+"/"+shortFile] = s + "\n"
	}
	for name, body := range files {
		w, err := zw.Create(name)
		if err == nil {
			_, err = w.Write([]byte(body))
		}
		if err != nil {
			return llmtool.Result{Text: "gagal menyusun zip skill: " + err.Error(), IsError: true}
		}
	}
	if err := zw.Close(); err != nil {
		return llmtool.Result{Text: "gagal menyusun zip skill: " + err.Error(), IsError: true}
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		return llmtool.Result{Text: "zip skill rusak: " + err.Error(), IsError: true}
	}
	res, err := t.svc.Upload(zr, UploadInput{Overwrite: a.Overwrite})
	if err != nil {
		var ae *apperror.AppError
		if errors.As(err, &ae) {
			msg := ae.Message
			for _, fe := range ae.Errors {
				msg += "; " + fe.Message
			}
			if ae.StatusCode == 409 {
				msg += ". Tanyakan ke user apakah skill lama boleh diganti; bila ya, panggil lagi dengan overwrite=true."
			}
			return llmtool.Result{Text: "gagal memasang skill: " + msg, IsError: true}
		}
		return llmtool.Result{Text: "gagal memasang skill: " + err.Error(), IsError: true}
	}

	zipURL := t.baseURL + "/api/skills/export?names=" + url.QueryEscape(a.Name)
	status := "baru"
	if len(res.Skills) > 0 && res.Skills[0].Replaced {
		status = "menggantikan versi lama"
	}
	logger.Info("skill dibuat lewat chat", zap.String("skill", a.Name), zap.Int("chars", len(a.Content)))
	return llmtool.Result{Text: fmt.Sprintf("Skill %s berhasil dibuat dan dipasang (%s, %d karakter, SHORT.md: %v). "+
		"Skill langsung dipakai otomatis di chat berikutnya bila pesannya cocok.\nLink download zip: %s\n"+
		"Tampilkan ke user sebagai link markdown [Download zip skill %s](%s), lalu ringkas isi skill-nya dalam beberapa poin.",
		a.Name, status, len([]rune(a.Content)), a.Short != "", zipURL, a.Name, zipURL)}
}
