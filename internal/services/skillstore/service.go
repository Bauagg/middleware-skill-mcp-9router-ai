package skillstore

import (
	"archive/zip"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/Bauagg/middleware-skill-ai/internal/logger"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/apperror"
	"github.com/Bauagg/middleware-skill-ai/internal/pkg/frontmatter"
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
