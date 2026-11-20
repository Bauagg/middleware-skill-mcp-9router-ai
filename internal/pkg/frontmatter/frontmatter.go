// Package frontmatter membaca frontmatter YAML di awal SKILL.md.
// Dipakai bersama oleh upload skill, daftar skill, dan index skill.
package frontmatter

import (
	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

// Meta adalah metadata skill dari frontmatter.
type Meta struct {
	Name        string
	Description string
	// Keywords bersifat opsional: kata kunci tambahan untuk pencarian, misalnya
	// padanan bahasa Indonesia dari deskripsi yang berbahasa Inggris.
	Keywords []string
}

// Parse membaca frontmatter dari isi SKILL.md. Error-nya berupa pesan siap
// tampil (bahasa Indonesia).
func Parse(data []byte) (Meta, error) {
	s := strings.TrimPrefix(string(data), "\xef\xbb\xbf") // buang BOM UTF-8 bila ada
	s = strings.ReplaceAll(s, "\r\n", "\n")

	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return Meta{}, errors.New("SKILL.md harus diawali frontmatter '---'")
	}
	block, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return Meta{}, errors.New("penutup frontmatter '---' tidak ditemukan")
	}

	var fm struct {
		Name        string      `yaml:"name"`
		Description string      `yaml:"description"`
		Keywords    keywordList `yaml:"keywords"`
	}
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return Meta{}, errors.New("YAML frontmatter tidak bisa dibaca")
	}

	return Meta{
		Name:        strings.TrimSpace(fm.Name),
		Description: strings.TrimSpace(fm.Description),
		Keywords:    fm.Keywords,
	}, nil
}

// Body mengembalikan isi SKILL.md tanpa frontmatter, sudah di-trim. Bila tidak
// ada frontmatter yang utuh, seluruh isi dikembalikan.
func Body(data []byte) string {
	s := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	s = strings.ReplaceAll(s, "\r\n", "\n")

	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return strings.TrimSpace(s)
	}
	_, after, ok := strings.Cut(rest, "\n---")
	if !ok {
		return strings.TrimSpace(s)
	}

	// buang sisa baris penutup ("---" boleh diikuti spasi)
	if i := strings.IndexByte(after, '\n'); i >= 0 {
		after = after[i+1:]
	} else {
		after = ""
	}
	return strings.TrimSpace(after)
}

// keywordList menerima keywords sebagai daftar YAML atau teks dipisah koma.
type keywordList []string

func (k *keywordList) UnmarshalYAML(n *yaml.Node) error {
	var items []string

	switch n.Kind {
	case yaml.ScalarNode:
		items = strings.FieldsFunc(n.Value, func(r rune) bool { return r == ',' || r == '\n' })
	case yaml.SequenceNode:
		if err := n.Decode(&items); err != nil {
			return err
		}
	default:
		return errors.New("keywords harus berupa daftar atau teks")
	}

	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			*k = append(*k, item)
		}
	}
	return nil
}
