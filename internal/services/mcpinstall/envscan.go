package mcpinstall

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Batas pembacaan kode repo untuk mencari variabel lingkungan.
const (
	scanMaxFiles    = 3000
	scanMaxFileSize = 512 << 10
	scanMaxKeys     = 40
	scanSnippets    = 2   // potongan kode per variabel
	scanSnippetLen  = 180 // karakter per potongan
)

// reEnvUse mengenali pembacaan variabel lingkungan di kode JS/TS, Python, Go,
// Rust, Java, dan Ruby.
var reEnvUse = []*regexp.Regexp{
	regexp.MustCompile(`process\.env\.([A-Z][A-Z0-9_]+)`),
	regexp.MustCompile(`process\.env\[\s*['"]([A-Z][A-Z0-9_]+)['"]\s*\]`),
	regexp.MustCompile(`os\.(?:environ\.get|getenv)\(\s*['"]([A-Z][A-Z0-9_]+)['"]`),
	regexp.MustCompile(`os\.environ\[\s*['"]([A-Z][A-Z0-9_]+)['"]\s*\]`),
	regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\(\s*"([A-Z][A-Z0-9_]+)"`),
	regexp.MustCompile(`env::var\(\s*"([A-Z][A-Z0-9_]+)"`),
	regexp.MustCompile(`System\.getenv\(\s*"([A-Z][A-Z0-9_]+)"`),
	regexp.MustCompile(`ENV\[\s*['"]([A-Z][A-Z0-9_]+)['"]\s*\]`),
}

var scanExt = map[string]bool{".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".mts": true, ".tsx": true,
	".py": true, ".go": true, ".rs": true, ".java": true, ".kt": true, ".rb": true}

var scanSkipDir = map[string]bool{"node_modules": true, ".git": true, "dist": true, "build": true, "vendor": true,
	".venv": true, "venv": true, "__pycache__": true, "target": true, "coverage": true, ".next": true}

// envNoise: variabel umum yang bukan data akses user.
var envNoise = map[string]bool{"NODE_ENV": true, "DEBUG": true, "CI": true, "HOME": true, "PATH": true, "PWD": true,
	"TERM": true, "TZ": true, "LANG": true, "SHELL": true, "USER": true, "HOSTNAME": true, "NODE_OPTIONS": true,
	"VERBOSE": true, "LOG_LEVEL": true, "npm_package_version": true}

// scanEnv membaca bagaimana repo memakai variabel lingkungan: komentar dan
// contoh nilai di .env.example, potongan kode yang membacanya, dan baris
// README yang menyebutnya. Bahan ini dipakai model untuk menulis panduan
// isian yang dipahami user awam.
func scanEnv(dir, envExample, readme string) []EnvHint {
	hints := map[string]*EnvHint{}
	get := func(k string) *EnvHint {
		h := hints[k]
		if h == nil {
			h = &EnvHint{Key: k}
			hints[k] = h
		}
		return h
	}

	if envExample != "" {
		exampleHints(envExample, get)
	}

	files := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if scanSkipDir[d.Name()] || (strings.HasPrefix(d.Name(), ".") && p != dir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !scanExt[strings.ToLower(filepath.Ext(p))] || strings.Contains(d.Name(), ".test.") || strings.Contains(d.Name(), "_test.") {
			return nil
		}
		if files++; files > scanMaxFiles {
			return filepath.SkipAll
		}
		if info, err := d.Info(); err != nil || info.Size() > scanMaxFileSize {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		codeHints(p, rel, get)
		return nil
	})

	for i, line := range strings.Split(readme, "\n") {
		for k, h := range hints {
			if len(h.Readme) < scanSnippets && strings.Contains(line, k) {
				h.Readme = append(h.Readme, "README:"+strconv.Itoa(i+1)+": "+clipLine(line))
			}
		}
	}

	keys := make([]string, 0, len(hints))
	for k := range hints {
		if !envNoise[k] && !reservedEnv[k] && !strings.HasPrefix(k, "MCP_") {
			keys = append(keys, k)
		}
	}
	// yang ada di .env.example lebih dulu, lalu yang paling sering dibaca kode
	sort.SliceStable(keys, func(i, j int) bool {
		a, b := hints[keys[i]], hints[keys[j]]
		if a.InExample != b.InExample {
			return a.InExample
		}
		return keys[i] < keys[j]
	})
	if len(keys) > scanMaxKeys {
		keys = keys[:scanMaxKeys]
	}
	out := make([]EnvHint, 0, len(keys))
	for _, k := range keys {
		h := *hints[k]
		h.Auto = autoEnv[k]
		out = append(out, h)
	}
	return out
}

// exampleHints membaca .env.example: komentar tepat di atas variabel dan
// contoh nilainya.
func exampleHints(file string, get func(string) *EnvHint) {
	f, err := os.Open(file)
	if err != nil {
		return
	}
	defer f.Close()
	var comment []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#"):
			if c := strings.TrimSpace(strings.TrimLeft(line, "#")); c != "" {
				comment = append(comment, c)
			}
			continue
		case line == "":
			comment = nil
			continue
		}
		if m := envLineRe.FindStringSubmatch(line); m != nil {
			h := get(m[1])
			h.InExample = true
			if len(comment) > 0 {
				h.Comment = clipLine(strings.Join(comment, " "))
			}
			if _, v, ok := strings.Cut(line, "="); ok {
				h.Example = clipLine(strings.Trim(strings.TrimSpace(v), `"'`))
			}
		}
		comment = nil
	}
}

func codeHints(path, rel string, get func(string) *EnvHint) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if !strings.Contains(line, "env") && !strings.Contains(line, "ENV") {
			continue
		}
		snippet := rel + ":" + strconv.Itoa(n) + ": " + clipLine(line)
		for _, re := range reEnvUse {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				// satu baris bisa membaca variabel yang sama lebih dari sekali
				if h := get(m[1]); len(h.Code) < scanSnippets && !slices.Contains(h.Code, snippet) {
					h.Code = append(h.Code, snippet)
				}
			}
		}
	}
}

func clipLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > scanSnippetLen {
		return string(r[:scanSnippetLen]) + "…"
	}
	return s
}
