package mcpinstall

// AnalyzeInput adalah body POST /api/mcp/installs/analyze.
type AnalyzeInput struct {
	RepoURL string `json:"repo_url" binding:"required,max=500"`
}

// Analysis adalah hasil pemeriksaan repo (tanpa menjalankan kode apa pun).
type Analysis struct {
	RepoURL        string            `json:"repo_url"`
	Owner          string            `json:"owner"`
	Repo           string            `json:"repo"`
	CommitSHA      string            `json:"commit_sha"`
	Folder         string            `json:"folder"` // lokasi clone di open_source_mcp
	Runtime        string            `json:"runtime"`
	PackageManager string            `json:"package_manager,omitempty"`
	PackageName    string            `json:"package_name,omitempty"`
	Scripts        map[string]string `json:"scripts,omitempty"`
	Bin            map[string]string `json:"bin,omitempty"`
	NodeEngine     string            `json:"node_engine,omitempty"`
	PythonScripts  []string          `json:"python_scripts,omitempty"`
	HasDockerfile  bool              `json:"has_dockerfile"`
	EnvExample     []string          `json:"env_example,omitempty"` // nama variabel dari .env.example
	Readme         string            `json:"readme,omitempty"`      // potongan awal README
	Files          int               `json:"files"`
	// EnvHints: hasil membaca kode tentang setiap variabel lingkungan, bahan
	// panduan isian untuk user.
	EnvHints []EnvHint `json:"env_hints,omitempty"`
}

// EnvHint adalah bacaan repo tentang satu variabel lingkungan.
type EnvHint struct {
	Key       string   `json:"key"`
	InExample bool     `json:"in_example"`        // tercantum di .env.example
	Auto      bool     `json:"auto,omitempty"`    // diisi middleware (PORT, TRANSPORT)
	Comment   string   `json:"comment,omitempty"` // komentar di atasnya di .env.example
	Example   string   `json:"example,omitempty"` // contoh nilai di .env.example
	Code      []string `json:"code,omitempty"`    // "file:baris: potongan kode" yang membacanya
	Readme    []string `json:"readme,omitempty"`  // baris README yang menyebutnya
}

// EnvGuide menjelaskan satu isian data akses kepada user awam (pemilik
// usaha, bukan programmer): ditampilkan di form.
type EnvGuide struct {
	Label string `json:"label"` // nama isian, mis. "Alamat server Kubernetes"
	Help  string `json:"help"`  // apa itu dan cara mendapatkannya
	// Example: contoh isian (palsu) sesuai format yang dibutuhkan, tampil
	// sebagai teks samar di dalam kotak isian.
	Example string `json:"example"`
	// Multiline: isian panjang (sertifikat, kubeconfig, JSON); form
	// menyediakan unggah file.
	Multiline bool `json:"multiline"`
}

// SubmitInput adalah body POST /api/mcp/installs (dan argumen tool chat).
type SubmitInput struct {
	RepoURL        string   `json:"repo_url" binding:"required,max=500"`
	Name           string   `json:"name" binding:"required,max=40"`
	InstallCommand string   `json:"install_command" binding:"max=2000"`
	StartCommand   string   `json:"start_command" binding:"required,max=2000"`
	Transport      string   `json:"transport"` // stdio (bawaan) | http
	HTTPPath       string   `json:"http_path"` // untuk transport http, bawaan /mcp
	EnvKeys        []string `json:"env_keys"`
	// EnvGuide: panduan setiap isian di EnvKeys untuk user awam (ditulis model
	// dari hasil membaca kode); yang kosong memakai panduan bawaan.
	EnvGuide map[string]EnvGuide `json:"env_guide"`
	Keywords []string            `json:"keywords"`
	Purpose  string              `json:"purpose" binding:"max=1000"`
	// Shared wajib dipilih admin: true = satu container berisi data rahasia
	// admin dipakai semua user; false = tiap user mengisi data rahasianya
	// sendiri dan mendapat container sendiri. Diabaikan untuk user biasa.
	Shared *bool `json:"shared"`
}

// ReviewInput adalah body POST /api/mcp/installs/:id/reject.
type ReviewInput struct {
	Reason string `json:"reason" binding:"max=1000"`
}

// View adalah tampilan satu pemasangan untuk API dan chat (tanpa isi rahasia).
type View struct {
	*Install
	Keywords []string `json:"keywords"`
	EnvKeys  []string `json:"env_keys"`
	// EnvLabels: nama isian yang mudah dibaca user (mis. DSN -> "Koneksi database")
	EnvLabels map[string]string `json:"env_labels"`
	// EnvGuide: panduan setiap isian (label, penjelasan, contoh)
	EnvGuide   map[string]EnvGuide `json:"env_guide"`
	EnvSet     []string            `json:"env_set"`     // variabel rahasia yang sudah diisi (nilainya tidak ditampilkan)
	EnvMissing []string            `json:"env_missing"` // yang belum diisi
	EnvURL     string              `json:"env_url,omitempty"`
	// Mine: instance milik user yang melihat (hanya untuk template tidak-sharing).
	Mine *Instance `json:"my_instance,omitempty"`
}
