// Package config memuat konfigurasi aplikasi dari environment / file .env.
// Ini padanan Go dari pola `dotenv` + `process.env` di template Express.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/Bauagg/middleware-skill-ai/internal/pkg/modelbudget"
)

// Config adalah akar konfigurasi aplikasi.
type Config struct {
	App       AppConfig
	DB        DBConfig
	Log       LogConfig
	Redis     RedisConfig
	Router    RouterConfig
	Skill     SkillConfig
	Embedding EmbeddingConfig
	Eval      EvalConfig
	Memory    MemoryConfig
	MCP       MCPConfig
}

// AppConfig berisi pengaturan runtime server.
type AppConfig struct {
	Env        string // development | production
	Port       string
	BaseURL    string
	GinMode    string // debug | release | test
	UploadPath string // folder skill (PATH_FILE_UPLOAD), dibuat otomatis saat start
}

// DBConfig berisi kredensial PostgreSQL.
type DBConfig struct {
	Host     string
	Port     string
	Name     string
	User     string
	Password string
	SSLMode  string
	Timezone string
}

// LogConfig berisi pengaturan logger.
type LogConfig struct {
	Level string
	Dir   string
}

// RedisConfig berisi pengaturan Redis untuk cache. Redis wajib: server tidak
// berjalan bila Addr kosong atau Redis tidak terjangkau saat start.
type RedisConfig struct {
	Addr     string // REDIS_ADDR (wajib), mis. localhost:6379
	Password string // REDIS_PASSWORD
	DB       int    // REDIS_DB

	// Timeout adalah batas waktu tiap operasi Redis. Kecil (200 ms) bila Redis
	// satu mesin/jaringan dengan server; perbesar bila Redis jauh (mis. Redis
	// DEV di VPS dipakai dari komputer lokal).
	Timeout time.Duration // REDIS_TIMEOUT
}

// RouterConfig berisi pengaturan 9Router.
type RouterConfig struct {
	// URL adalah alamat dasar 9Router tujuan proxy (AI_ROUTER_BASE_URL, wajib),
	// mis. https://9router.example.com/v1
	URL *url.URL

	// APIKey dipakai middleware untuk panggilannya sendiri ke 9Router
	// (pemilih cadangan dan eval), bukan untuk request client yang diteruskan
	// (AI_ROUTER_API_KEY, wajib).
	APIKey string

	// Model bawaan untuk panggilan middleware sendiri (AI_ROUTER_MODEL).
	Model string
}

// SkillConfig berisi pengaturan pemilihan dan penyisipan skill.
type SkillConfig struct {
	// --- seleksi BM25 ---

	// MinScore adalah threshold skor BM25: skill dengan skor di bawahnya tidak dipilih.
	MinScore float64 // SKILL_MIN_SCORE

	// SwitchRatio: skill aktif sebuah percakapan baru diganti bila skor skill
	// lain minimal sekian kali skor skill aktif ("jauh lebih cocok").
	SwitchRatio float64 // SKILL_SWITCH_RATIO

	// AmbiguousRatio: skill lain dianggap "bersaing" dengan skill teratas bila
	// skornya minimal sekian kali skor teratas (0-1). Dipakai untuk memutuskan
	// kapan pemilih cadangan dipanggil.
	AmbiguousRatio float64 // SKILL_AMBIGUOUS_RATIO

	// --- seleksi embedding (dipakai bila EMBEDDING_MODEL diisi) ---

	// MinSimilarity adalah threshold kemiripan kosinus (-1..1).
	MinSimilarity float64 // SKILL_MIN_SIMILARITY
	// SwitchMargin: padanan SwitchRatio untuk embedding (selisih kemiripan).
	SwitchMargin float64 // SKILL_SWITCH_MARGIN
	// AmbiguousMargin: padanan AmbiguousRatio untuk embedding (selisih kemiripan).
	AmbiguousMargin float64 // SKILL_AMBIGUOUS_MARGIN

	// --- pemilih cadangan ---

	// FallbackModel adalah model 9Router yang dipanggil hanya bila skor ambigu.
	// Kosong = pemilih cadangan dimatikan.
	FallbackModel   string        // SKILL_FALLBACK_MODEL
	FallbackTimeout time.Duration // SKILL_FALLBACK_TIMEOUT

	// Budgets adalah batas token skill per model.
	Budgets modelbudget.Budgets // SKILL_BUDGET_DEFAULT, SKILL_BUDGETS

	SessionTTL   time.Duration // SKILL_SESSION_TTL: masa berlaku skill aktif per percakapan
	SelectionTTL time.Duration // SKILL_SELECTION_TTL: masa berlaku cache hasil seleksi

	// WatchInterval: seberapa sering folder skill diperiksa untuk reload
	// otomatis. 0 = dimatikan.
	WatchInterval time.Duration // SKILL_WATCH_INTERVAL
}

// EmbeddingConfig berisi pengaturan embedding. Model kosong = embedding
// dimatikan (seleksi memakai BM25). Bila diisi, server embedding wajib
// terjangkau saat start.
type EmbeddingConfig struct {
	BaseURL *url.URL      // EMBEDDING_BASE_URL (bawaan: AI_ROUTER_BASE_URL)
	APIKey  string        // EMBEDDING_API_KEY (bawaan: AI_ROUTER_API_KEY)
	Model   string        // EMBEDDING_MODEL
	Timeout time.Duration // EMBEDDING_TIMEOUT: batas waktu embedding pesan per request
}

// EvalConfig berisi pengaturan eval skill.
type EvalConfig struct {
	TargetModel string // EVAL_TARGET_MODEL: model yang diuji (bawaan: AI_ROUTER_MODEL)
	JudgeModel  string // EVAL_JUDGE_MODEL: model penilai (bawaan: AI_ROUTER_MODEL)
	Concurrency int    // EVAL_CONCURRENCY: berapa pertanyaan diproses bersamaan
}

// MemoryConfig berisi pengaturan memori (Tahap 4): context pack dan catatan memori.
type MemoryConfig struct {
	ContextPackMaxTokens int           // CONTEXT_PACK_MAX_TOKENS: batas token gabungan context pack aktif (0 = tanpa batas)
	MaxNotes             int           // MEMORY_MAX_NOTES: catatan maksimal per request (0 = penyisipan catatan dimatikan)
	NoteMaxTokens        int           // MEMORY_NOTE_MAX_TOKENS: batas token per catatan di prompt
	MinMatch             int           // MEMORY_MIN_MATCH: kata pesan minimal yang harus ada di catatan
	LookupTimeout        time.Duration // MEMORY_LOOKUP_TIMEOUT: batas waktu pencarian catatan per request
	NoteTTL              time.Duration // MEMORY_NOTE_TTL: masa berlaku catatan sejak dikonfirmasi (0 = selamanya)

	ExtractModel       string        // MEMORY_EXTRACT_MODEL: model ekstraksi ("" = ekstraksi dimatikan)
	ExtractTimeout     time.Duration // MEMORY_EXTRACT_TIMEOUT: batas waktu satu ekstraksi
	ExtractConcurrency int           // MEMORY_EXTRACT_CONCURRENCY: ekstraksi yang boleh berjalan bersamaan
}

// MCPConfig berisi pengaturan koneksi ke server MCP (Figma, Canva, dll.).
// Daftar servernya ada di file MCP_CONFIG_PATH; token login di database.
type MCPConfig struct {
	ConfigPath      string        // MCP_CONFIG_PATH: file daftar server (tidak ada = MCP dimatikan)
	TokenKey        string        // MCP_TOKEN_KEY: kunci enkripsi token login (64 karakter hex)
	MaxTools        int           // MCP_MAX_TOOLS: tool maksimal yang dikirim ke model per request (0 = MCP tidak dipakai di chat)
	MinScore        float64       // MCP_TOOL_MIN_SCORE: skor BM25 minimal sebuah tool supaya dikirim
	MaxSteps        int           // MCP_MAX_STEPS: putaran panggil-tool maksimal per request
	ToolTimeout     time.Duration // MCP_TOOL_TIMEOUT: batas waktu satu pemanggilan tool
	ResultMaxTokens int           // MCP_TOOL_RESULT_MAX_TOKENS: batas token hasil satu tool yang diberikan ke model
	RefreshInterval time.Duration // MCP_REFRESH_INTERVAL: seberapa sering daftar tool diambil ulang (0 = hanya saat start/login)
}

// Load membaca .env (bila ada) lalu menyusun Config dari environment variable.
func Load() (*Config, error) {
	// Abaikan error bila .env tidak ada; environment variable tetap dibaca.
	_ = godotenv.Load()

	router, err := loadRouterConfig()
	if err != nil {
		return nil, err
	}

	skill, err := loadSkillConfig()
	if err != nil {
		return nil, err
	}

	embedding, err := loadEmbeddingConfig(router)
	if err != nil {
		return nil, err
	}

	eval, err := loadEvalConfig(router)
	if err != nil {
		return nil, err
	}

	memory, err := loadMemoryConfig()
	if err != nil {
		return nil, err
	}

	mcp, err := loadMCPConfig()
	if err != nil {
		return nil, err
	}

	redisAddr := strings.TrimSpace(getEnv("REDIS_ADDR", ""))
	if redisAddr == "" {
		return nil, fmt.Errorf("REDIS_ADDR wajib diisi (alamat Redis untuk cache, mis. localhost:6379)")
	}
	redisDB, err := getEnvInt("REDIS_DB", 0)
	if err != nil {
		return nil, err
	}
	redisTimeout, err := getEnvDuration("REDIS_TIMEOUT", 200*time.Millisecond)
	if err != nil {
		return nil, err
	}

	return &Config{
		App: AppConfig{
			Env:        getEnv("APP_ENV", "development"),
			Port:       getEnv("PORT", "4000"),
			BaseURL:    getEnv("BASE_URL", "http://localhost:4000"),
			GinMode:    getEnv("GIN_MODE", "debug"),
			UploadPath: getEnv("PATH_FILE_UPLOAD", "skills"),
		},
		DB: DBConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			Name:     getEnv("DB_NAME", ""),
			User:     getEnv("DB_USER", ""),
			Password: getEnv("DB_PASSWORD", ""),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
			Timezone: getEnv("DB_TIMEZONE", "Asia/Jakarta"),
		},
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
			Dir:   getEnv("LOG_DIR", "logs"),
		},
		Redis: RedisConfig{
			Addr:     redisAddr,
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       redisDB,
			Timeout:  redisTimeout,
		},
		Router:    router,
		Skill:     skill,
		Embedding: embedding,
		Eval:      eval,
		Memory:    memory,
		MCP:       mcp,
	}, nil
}

// loadRouterConfig membaca pengaturan AI_ROUTER_*.
func loadRouterConfig() (RouterConfig, error) {
	u, err := parseBaseURL("AI_ROUTER_BASE_URL", getEnv("AI_ROUTER_BASE_URL", ""))
	if err != nil {
		return RouterConfig{}, err
	}

	key := strings.TrimSpace(getEnv("AI_ROUTER_API_KEY", ""))
	if key == "" {
		return RouterConfig{}, fmt.Errorf("AI_ROUTER_API_KEY wajib diisi (dipakai pemilih cadangan dan eval)")
	}

	return RouterConfig{URL: u, APIKey: key, Model: strings.TrimSpace(getEnv("AI_ROUTER_MODEL", ""))}, nil
}

// loadSkillConfig membaca semua pengaturan SKILL_*.
func loadSkillConfig() (SkillConfig, error) {
	var c SkillConfig
	var err error

	if c.MinScore, err = getEnvFloat("SKILL_MIN_SCORE", 2.0); err != nil {
		return c, err
	}
	if c.SwitchRatio, err = getEnvFloat("SKILL_SWITCH_RATIO", 1.5); err != nil {
		return c, err
	}
	if c.SwitchRatio < 1 {
		return c, fmt.Errorf("SKILL_SWITCH_RATIO harus >= 1, bukan %v", c.SwitchRatio)
	}
	if c.AmbiguousRatio, err = getEnvFloat("SKILL_AMBIGUOUS_RATIO", 0.8); err != nil {
		return c, err
	}
	if c.AmbiguousRatio <= 0 || c.AmbiguousRatio > 1 {
		return c, fmt.Errorf("SKILL_AMBIGUOUS_RATIO harus di antara 0 dan 1, bukan %v", c.AmbiguousRatio)
	}

	if c.MinSimilarity, err = getEnvFloat("SKILL_MIN_SIMILARITY", 0.5); err != nil {
		return c, err
	}
	if c.SwitchMargin, err = getEnvFloat("SKILL_SWITCH_MARGIN", 0.05); err != nil {
		return c, err
	}
	if c.AmbiguousMargin, err = getEnvFloat("SKILL_AMBIGUOUS_MARGIN", 0.03); err != nil {
		return c, err
	}

	c.FallbackModel = strings.TrimSpace(getEnv("SKILL_FALLBACK_MODEL", ""))
	if c.FallbackTimeout, err = getEnvDuration("SKILL_FALLBACK_TIMEOUT", 5*time.Second); err != nil {
		return c, err
	}

	defaultBudget, err := getEnvInt("SKILL_BUDGET_DEFAULT", 3000)
	if err != nil {
		return c, err
	}
	if c.Budgets, err = modelbudget.Parse(getEnv("SKILL_BUDGETS", ""), defaultBudget); err != nil {
		return c, fmt.Errorf("SKILL_BUDGETS: %w", err)
	}

	if c.SessionTTL, err = getEnvDuration("SKILL_SESSION_TTL", 45*time.Minute); err != nil {
		return c, err
	}
	if c.SelectionTTL, err = getEnvDuration("SKILL_SELECTION_TTL", time.Hour); err != nil {
		return c, err
	}
	c.WatchInterval, err = getEnvDuration("SKILL_WATCH_INTERVAL", 5*time.Second)
	return c, err
}

// loadEmbeddingConfig membaca pengaturan EMBEDDING_*. Alamat dan key bawaan
// mengikuti 9Router, jadi cukup isi EMBEDDING_MODEL bila 9Router sudah punya
// provider embedding; untuk server lain (mis. Ollama) isi juga EMBEDDING_BASE_URL.
func loadEmbeddingConfig(router RouterConfig) (EmbeddingConfig, error) {
	c := EmbeddingConfig{
		BaseURL: router.URL,
		APIKey:  strings.TrimSpace(getEnv("EMBEDDING_API_KEY", router.APIKey)),
		Model:   strings.TrimSpace(getEnv("EMBEDDING_MODEL", "")),
	}

	if raw := getEnv("EMBEDDING_BASE_URL", ""); raw != "" {
		u, err := parseBaseURL("EMBEDDING_BASE_URL", raw)
		if err != nil {
			return c, err
		}
		c.BaseURL = u
	}

	var err error
	c.Timeout, err = getEnvDuration("EMBEDDING_TIMEOUT", 2*time.Second)
	return c, err
}

// loadEvalConfig membaca pengaturan EVAL_*.
func loadEvalConfig(router RouterConfig) (EvalConfig, error) {
	c := EvalConfig{
		TargetModel: strings.TrimSpace(getEnv("EVAL_TARGET_MODEL", router.Model)),
		JudgeModel:  strings.TrimSpace(getEnv("EVAL_JUDGE_MODEL", router.Model)),
	}

	var err error
	if c.Concurrency, err = getEnvInt("EVAL_CONCURRENCY", 3); err != nil {
		return c, err
	}
	if c.Concurrency < 1 {
		c.Concurrency = 1
	}
	return c, nil
}

func loadMemoryConfig() (MemoryConfig, error) {
	c := MemoryConfig{ExtractModel: strings.TrimSpace(getEnv("MEMORY_EXTRACT_MODEL", ""))}

	ints := []struct {
		key string
		dst *int
		def int
	}{
		{"CONTEXT_PACK_MAX_TOKENS", &c.ContextPackMaxTokens, 800},
		{"MEMORY_MAX_NOTES", &c.MaxNotes, 2},
		{"MEMORY_NOTE_MAX_TOKENS", &c.NoteMaxTokens, 150},
		{"MEMORY_MIN_MATCH", &c.MinMatch, 2},
		{"MEMORY_EXTRACT_CONCURRENCY", &c.ExtractConcurrency, 2},
	}
	for _, it := range ints {
		v, err := getEnvInt(it.key, it.def)
		if err != nil {
			return c, err
		}
		if v < 0 {
			return c, fmt.Errorf("%s tidak boleh negatif", it.key)
		}
		*it.dst = v
	}

	durs := []struct {
		key string
		dst *time.Duration
		def time.Duration
	}{
		{"MEMORY_LOOKUP_TIMEOUT", &c.LookupTimeout, 300 * time.Millisecond},
		{"MEMORY_NOTE_TTL", &c.NoteTTL, 90 * 24 * time.Hour},
		{"MEMORY_EXTRACT_TIMEOUT", &c.ExtractTimeout, 60 * time.Second},
	}
	for _, it := range durs {
		v, err := getEnvDuration(it.key, it.def)
		if err != nil {
			return c, err
		}
		*it.dst = v
	}

	if c.ExtractConcurrency < 1 {
		c.ExtractConcurrency = 1
	}
	if c.LookupTimeout <= 0 {
		c.LookupTimeout = 300 * time.Millisecond
	}
	if c.ExtractTimeout <= 0 {
		c.ExtractTimeout = 60 * time.Second
	}
	return c, nil
}

func loadMCPConfig() (MCPConfig, error) {
	c := MCPConfig{
		ConfigPath: strings.TrimSpace(getEnv("MCP_CONFIG_PATH", "mcp.json")),
		TokenKey:   strings.TrimSpace(getEnv("MCP_TOKEN_KEY", "")),
	}

	var err error
	if c.MaxTools, err = getEnvInt("MCP_MAX_TOOLS", 8); err != nil {
		return c, err
	}
	if c.MinScore, err = getEnvFloat("MCP_TOOL_MIN_SCORE", 1.0); err != nil {
		return c, err
	}
	if c.MaxSteps, err = getEnvInt("MCP_MAX_STEPS", 6); err != nil {
		return c, err
	}
	if c.ToolTimeout, err = getEnvDuration("MCP_TOOL_TIMEOUT", 60*time.Second); err != nil {
		return c, err
	}
	if c.ResultMaxTokens, err = getEnvInt("MCP_TOOL_RESULT_MAX_TOKENS", 4000); err != nil {
		return c, err
	}
	if c.RefreshInterval, err = getEnvDuration("MCP_REFRESH_INTERVAL", 30*time.Minute); err != nil {
		return c, err
	}

	if c.MaxTools < 0 || c.MaxSteps < 0 || c.ResultMaxTokens < 0 {
		return c, fmt.Errorf("MCP_MAX_TOOLS, MCP_MAX_STEPS, dan MCP_TOOL_RESULT_MAX_TOKENS tidak boleh negatif")
	}
	if c.MaxSteps == 0 {
		c.MaxSteps = 1
	}
	if c.ToolTimeout <= 0 {
		c.ToolTimeout = 60 * time.Second
	}
	return c, nil
}

// DSN menyusun connection string PostgreSQL untuk GORM.
func (d DBConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=%s TimeZone=%s",
		d.Host, d.User, d.Password, d.Name, d.Port, d.SSLMode, d.Timezone,
	)
}

// parseBaseURL memvalidasi alamat dasar sebuah layanan (wajib diisi). Lebih
// baik gagal saat start daripada saat request.
func parseBaseURL(key, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%s wajib diisi (mis. https://host/v1)", key)
	}

	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s tidak valid: %q (harus http(s)://host[/path])", key, raw)
	}

	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// getEnvFloat membaca env sebagai angka desimal; nilai yang bukan angka
// membuat start gagal supaya salah ketik tidak diam-diam memakai default.
func getEnvFloat(key string, def float64) (float64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}

	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%s harus berupa angka >= 0, bukan %q", key, v)
	}
	return f, nil
}

// getEnvInt membaca env sebagai bilangan bulat >= 0.
func getEnvInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}

	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s harus berupa bilangan bulat >= 0, bukan %q", key, v)
	}
	return n, nil
}

// getEnvDuration membaca env sebagai durasi Go (mis. 30s, 45m, 1h); "0" = nol.
func getEnvDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def, nil
	}

	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s harus berupa durasi >= 0 seperti 30s, 45m, atau 1h, bukan %q", key, v)
	}
	return d, nil
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}
