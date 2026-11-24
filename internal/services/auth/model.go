package auth

// User adalah satu baris tabel m_users: akun yang mendaftar langsung di
// middleware (web middleware AI), untuk user yang tidak datang lewat aplikasi
// pihak ketiga. Kolomnya sengaja hanya id, email, password, dan role.
type User struct {
	ID    string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Email string `json:"email" gorm:"size:254;not null;uniqueIndex"`
	// Password berisi hash bcrypt, tidak pernah dikirim di respons.
	Password string `json:"-" gorm:"size:100;not null"`
	// Role: user | admin. Admin = pemilik aplikasi.
	Role string `json:"role" gorm:"size:10;not null;default:'user'"`
}

// TableName menetapkan nama tabel. Data master diberi awalan "m_".
func (User) TableName() string { return "m_users" }
