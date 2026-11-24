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
	Keywords       []string `json:"keywords"`
	Purpose        string   `json:"purpose" binding:"max=1000"`
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
	Keywords   []string `json:"keywords"`
	EnvKeys    []string `json:"env_keys"`
	EnvSet     []string `json:"env_set"`     // variabel rahasia yang sudah diisi (nilainya tidak ditampilkan)
	EnvMissing []string `json:"env_missing"` // yang belum diisi
	EnvURL     string   `json:"env_url,omitempty"`
	// Mine: instance milik user yang melihat (hanya untuk template tidak-sharing).
	Mine *Instance `json:"my_instance,omitempty"`
}
