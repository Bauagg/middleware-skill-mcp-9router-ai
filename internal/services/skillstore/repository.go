package skillstore

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DirStat adalah ringkasan isi satu folder skill.
type DirStat struct {
	Files    int
	Bytes    int64
	ModTime  time.Time // waktu ubah terbaru dari semua file di dalamnya
	HasShort bool
}

// Repository adalah lapisan akses penyimpanan skill (folder di disk).
// Padanan repository di template, tapi sumber datanya file, bukan database.
type Repository struct {
	root string
}

// NewRepository membuat repository untuk folder skill root.
func NewRepository(root string) *Repository {
	return &Repository{root: root}
}

// Exists memeriksa apakah skill dengan nama tersebut sudah terpasang.
func (r *Repository) Exists(name string) bool {
	_, err := os.Stat(filepath.Join(r.root, name))
	return err == nil
}

// List mengembalikan nama folder skill yang terpasang, terurut. Folder
// berawalan "." (staging dan backup upload) tidak dihitung.
func (r *Repository) List() ([]string, error) {
	entries, err := os.ReadDir(r.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Signature mengembalikan sidik jari isi folder skill (path, ukuran, dan waktu
// ubah tiap file). Nilainya berubah bila ada file yang ditambah, diubah, atau
// dihapus, jadi cukup untuk mendeteksi perubahan tanpa membaca isi file.
// Folder dan file berawalan "." (staging, backup upload, cache embedding)
// diabaikan, supaya tulisan middleware sendiri tidak memicu reload.
func (r *Repository) Signature() (string, error) {
	h := sha1.New()

	err := filepath.WalkDir(r.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == r.root && os.IsNotExist(err) {
				return filepath.SkipAll // folder belum ada: sidik jari kosong
			}
			if os.IsNotExist(err) {
				return nil // file hilang saat dipindai, akan terlihat di pemindaian berikutnya
			}
			return err
		}

		if d.IsDir() {
			if p != r.root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}

		rel, _ := filepath.Rel(r.root, p)
		fmt.Fprintf(h, "%s|%d|%d\n", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FileExists memeriksa apakah file ada di dalam folder skill.
func (r *Repository) FileExists(name, file string) bool {
	info, err := os.Stat(filepath.Join(r.root, name, file))
	return err == nil && info.Mode().IsRegular()
}

// ReadFile membaca satu file di dalam folder skill dengan batas ukuran.
func (r *Repository) ReadFile(name, file string) ([]byte, error) {
	f, err := os.Open(filepath.Join(r.root, name, file))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(io.LimitReader(f, maxFileBytes))
}

// WriteFile menulis file di dalam folder skill secara atomik: ditulis ke file
// sementara dulu lalu di-rename, supaya pembaca tidak pernah melihat file
// setengah jadi.
func (r *Repository) WriteFile(name, file string, data []byte) error {
	dir := filepath.Join(r.root, name)

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		return errors.Join(werr, cerr)
	}

	if err := os.Rename(tmpName, filepath.Join(dir, file)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Stat meringkas isi folder skill. Symlink tidak diikuti dan tidak dihitung;
// file berawalan "." (mis. cache embedding) juga tidak dihitung.
func (r *Repository) Stat(name string) (DirStat, error) {
	var st DirStat
	base := filepath.Join(r.root, name)

	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}

		st.Files++
		st.Bytes += info.Size()
		if info.ModTime().After(st.ModTime) {
			st.ModTime = info.ModTime()
		}
		if rel, _ := filepath.Rel(base, p); rel == shortFile {
			st.HasShort = true
		}
		return nil
	})
	return st, err
}

// NewStagingDir membuat folder sementara di dalam folder skill. Letaknya
// sengaja satu volume dengan tujuan supaya rename bersifat atomik.
// Folder berawalan "." (.upload-*, .old-*) harus diabaikan oleh loader skill.
func (r *Repository) NewStagingDir() (string, error) {
	if err := os.MkdirAll(r.root, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(r.root, ".upload-")
}

// Swap memasang folder staging sebagai skill `name`. Bila overwrite dan skill
// lama ada, skill lama dipindah ke backup dulu (dikembalikan lewat backup)
// agar bisa dipulihkan bila ada langkah lain yang gagal.
func (r *Repository) Swap(staged, name string, overwrite bool) (backup string, err error) {
	target := filepath.Join(r.root, name)

	if overwrite && r.Exists(name) {
		backup = filepath.Join(r.root, fmt.Sprintf(".old-%s-%d", name, time.Now().UnixNano()))
		if err := os.Rename(target, backup); err != nil {
			return "", err
		}
	}

	// rename ke folder yang sudah ada gagal (bukan menimpa), jadi aman dari race
	if err := os.Rename(staged, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return "", err
	}
	return backup, nil
}

// Rollback membatalkan Swap yang sudah berhasil: skill baru dihapus dan
// skill lama (jika ada) dikembalikan.
func (r *Repository) Rollback(name, backup string) {
	target := filepath.Join(r.root, name)
	_ = os.RemoveAll(target)
	if backup != "" {
		_ = os.Rename(backup, target)
	}
}

// Discard menghapus backup setelah semua skill sukses dipasang.
func (r *Repository) Discard(backup string) {
	if backup != "" {
		_ = os.RemoveAll(backup)
	}
}
