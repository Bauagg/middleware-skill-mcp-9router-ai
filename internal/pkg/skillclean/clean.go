// Package skillclean membersihkan isi SKILL.md sebelum disisipkan ke prompt:
// membuang komentar dan bagian yang hanya berlaku di Claude (path /mnt/skills,
// variabel ${CLAUDE_...}, nama tool khusus, dan blok kode yang menjalankan
// script), karena model lain di belakang 9Router tidak punya sandbox ataupun
// tool tersebut dan bagian itu hanya membingungkan serta memakai token.
//
// Menambah aturan baru cukup menambah satu baris di lineRules atau scriptRules.
package skillclean

import (
	"regexp"
	"strings"
)

// Stats melaporkan apa yang dibuang.
type Stats struct {
	Lines  int // baris biasa yang dibuang
	Blocks int // blok kode yang dibuang
}

// lineRules: baris yang cocok dibuang (juga membuat blok kode yang memuatnya
// dibuang seluruhnya, supaya blok tidak tinggal setengah).
var lineRules = []*regexp.Regexp{
	// path khusus lingkungan Claude
	regexp.MustCompile(`/mnt/(skills|user-data|data|outputs)\b`),
	// variabel lingkungan Claude, mis. ${CLAUDE_SKILL_DIR}
	regexp.MustCompile(`\$\{?CLAUDE_[A-Z0-9_]+\}?`),
	// nama tool bawaan Claude Code
	regexp.MustCompile(`\b(AskUserQuestion|TodoWrite|WebSearch|WebFetch|NotebookEdit|ExitPlanMode)\b`),
	regexp.MustCompile(`\bsubagent_type\b`),
	regexp.MustCompile(`\b(Agent|Task) tool\b`),
	// tool MCP
	regexp.MustCompile(`\bmcp__[A-Za-z0-9_]+`),
}

// scriptRules: dipakai hanya pada blok kode. Blok yang berisi perintah
// menjalankan program dibuang seluruhnya karena model tidak bisa menjalankannya.
var scriptRules = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^\s*(\$\s+)?(python3?|node|bash|sh|ruby|npx|npm|pip3?)\s+\S`),
}

var (
	commentRe  = regexp.MustCompile(`(?s)<!--.*?-->`)
	blankRunRe = regexp.MustCompile(`\n{3,}`)
)

// Clean mengembalikan teks yang sudah dibersihkan beserta ringkasan yang dibuang.
func Clean(s string) (string, Stats) {
	var st Stats

	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = commentRe.ReplaceAllString(s, "")

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))

	for i := 0; i < len(lines); {
		marker := fenceMarker(lines[i])
		if marker == "" {
			if matchesAny(lineRules, lines[i]) {
				st.Lines++
			} else {
				out = append(out, lines[i])
			}
			i++
			continue
		}

		// blok kode: dari baris pembuka sampai penutup (atau akhir teks)
		end := len(lines) - 1
		for j := i + 1; j < len(lines); j++ {
			if fenceMarker(lines[j]) == marker {
				end = j
				break
			}
		}

		block := lines[i : end+1]
		if dropBlock(block) {
			st.Blocks++
		} else {
			out = append(out, block...)
		}
		i = end + 1
	}

	text := strings.Join(dropEmptyHeadings(out), "\n")
	text = blankRunRe.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text), st
}

func matchesAny(rules []*regexp.Regexp, s string) bool {
	for _, r := range rules {
		if r.MatchString(s) {
			return true
		}
	}
	return false
}

// fenceMarker mengembalikan penanda blok kode di awal baris ("```" atau "~~~"), "" bila bukan.
func fenceMarker(line string) string {
	t := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(t, "```"):
		return "```"
	case strings.HasPrefix(t, "~~~"):
		return "~~~"
	}
	return ""
}

// dropBlock memutuskan apakah sebuah blok kode dibuang.
func dropBlock(block []string) bool {
	body := strings.Join(block, "\n")
	return matchesAny(scriptRules, body) || matchesAny(lineRules, body)
}

// headingLevel mengembalikan tingkat judul markdown ("## x" = 2), 0 bila bukan judul.
func headingLevel(line string) int {
	t := strings.TrimLeft(line, " ")
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	if n > 0 && n <= 6 && len(t) > n && t[n] == ' ' {
		return n
	}
	return 0
}

// dropEmptyHeadings membuang judul yang tidak punya isi lagi setelah
// pembersihan (judul yang langsung disusul judul sejajar/lebih tinggi atau
// akhir teks). Isi blok kode tidak dianggap judul.
func dropEmptyHeadings(lines []string) []string {
	for changed := true; changed; {
		changed = false

		levels := make([]int, len(lines))
		inFence := false
		for i, l := range lines {
			if m := fenceMarker(l); m != "" {
				inFence = !inFence
				continue
			}
			if !inFence {
				levels[i] = headingLevel(l)
			}
		}

		kept := make([]string, 0, len(lines))
		for i, l := range lines {
			if levels[i] > 0 && isEmptyHeading(lines, levels, i) {
				changed = true
				continue
			}
			kept = append(kept, l)
		}
		lines = kept
	}
	return lines
}

// isEmptyHeading: judul pada indeks i kosong bila baris tak-kosong berikutnya
// adalah judul sejajar/lebih tinggi, atau tidak ada lagi.
func isEmptyHeading(lines []string, levels []int, i int) bool {
	for j := i + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			continue
		}
		return levels[j] > 0 && levels[j] <= levels[i]
	}
	return true
}
