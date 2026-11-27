package mcpinstall

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// autoEnv adalah pengaturan server MCP itu sendiri (port dan alamat tempat
// ia mendengarkan, transport), yang diisi middleware saat container
// dijalankan (lihat serverEnv), jadi tidak pernah ditanyakan atau
// ditampilkan ke user: port server kita bukan urusan user dan berbahaya bila
// diketahui. Port milik layanan user (DB_PORT, SSH_PORT, ...) tetap ditanyakan.
var autoEnv = map[string]bool{
	"PORT": true, "HTTP_PORT": true, "SERVER_PORT": true, "LISTEN_PORT": true, "APP_PORT": true,
	"BIND": true, "BIND_HOST": true, "BIND_ADDR": true, "BIND_ADDRESS": true,
	"LISTEN_HOST": true, "LISTEN_ADDR": true, "LISTEN_ADDRESS": true,
	"TRANSPORT": true,
}

// serverEnv mengisi pengaturan server MCP yang dibaca repo: port container,
// mendengarkan di semua alamat di dalam container (port container hanya
// dibuka ke middleware), dan transport yang sama dengan runner.
func serverEnv(key, transport string, port int) (string, bool) {
	switch key {
	case "PORT":
		return "", false // selalu diisi containerEnv
	case "TRANSPORT":
		return transport, true
	case "HTTP_PORT", "SERVER_PORT", "LISTEN_PORT", "APP_PORT":
		return strconv.Itoa(port), true
	case "BIND", "BIND_HOST", "BIND_ADDR", "BIND_ADDRESS", "LISTEN_HOST", "LISTEN_ADDR", "LISTEN_ADDRESS":
		return "0.0.0.0", true
	}
	return "", false
}

// userKeys membuang variabel otomatis dari daftar variabel install.
func userKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !autoEnv[k] {
			out = append(out, k)
		}
	}
	return out
}

// reConnKey mengenali variabel berisi alamat koneksi database (DSN, URL).
var reConnKey = regexp.MustCompile(`^(?:[A-Z0-9]+_)*(?:DSN|DATABASE_URL|DATABASE_URI|DB_URL|DB_URI|CONNECTION_STRING|CONN_STR|CONN_STRING|` +
	`POSTGRES_URL|POSTGRESQL_URL|PG_URL|PG_CONNECTION_STRING|MYSQL_URL|MARIADB_URL|MONGODB_URI|MONGODB_URL|MONGO_URI|MONGO_URL|MSSQL_URL|SQLSERVER_URL)$`)

// isConnKey melaporkan apakah variabel ini alamat koneksi database: user
// cukup menjelaskan database-nya, middleware yang menyusun alamatnya.
func isConnKey(k string) bool { return reConnKey.MatchString(k) }

// envLabel adalah nama isian yang mudah dibaca user, bukan nama variabel.
func envLabel(k string) string {
	if isConnKey(k) {
		return "Koneksi database"
	}
	words := strings.Split(strings.ToLower(k), "_")
	for i, w := range words {
		switch w {
		case "api", "url", "uri", "id", "dsn", "sql", "ssh", "sftp", "ftp", "smtp", "imap", "pop3", "ldap", "mqtt",
			"amqp", "aws", "gcp", "ai", "ca", "ssl", "tls", "jwt", "oauth", "pat", "s3":
			words[i] = strings.ToUpper(w)
		}
	}
	s := strings.Join(words, " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// labelsOf mengambil label setiap isian dari panduannya, untuk FE.
func labelsOf(guide map[string]EnvGuide) map[string]string {
	m := make(map[string]string, len(guide))
	for k, g := range guide {
		m[k] = g.Label
	}
	return m
}

// dbInfo adalah data koneksi yang terbaca dari penjelasan user.
type dbInfo struct {
	kind, host, port, user, pass, name, ssl string
}

var dbKinds = []struct {
	re         *regexp.Regexp
	kind, port string
	label      string
}{
	{regexp.MustCompile(`(?i)\b(postgres(?:ql)?|psql|pg)\b`), "postgres", "5432", "PostgreSQL"},
	{regexp.MustCompile(`(?i)\bmariadb\b`), "mariadb", "3306", "MariaDB"},
	{regexp.MustCompile(`(?i)\bmysql\b`), "mysql", "3306", "MySQL"},
	{regexp.MustCompile(`(?i)\b(sql ?server|mssql|azure sql)\b`), "sqlserver", "1433", "SQL Server"},
	{regexp.MustCompile(`(?i)\bmongo(?:db)?\b`), "mongodb", "27017", "MongoDB"},
	{regexp.MustCompile(`(?i)\bsqlite\b`), "sqlite", "", "SQLite"},
}

// field membuat pola "<kata kunci>[nya] [:|=|adalah|yaitu] <nilai>"; nilai
// boleh dikutip. Kata kunci harus diikuti pemisah, supaya "db" tidak
// menempel ke "db.tokoku.com".
func field(keys string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[\s,;(])(?:` + keys + `)(?:nya)?(?:\s*[:=]\s*|\s+(?:adalah\s+|yaitu\s+|[:=]\s*)?)("[^"]*"|'[^']*'|[^\s,;]+)`)
}

var (
	reDBURL  = regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mariadb|sqlserver|mongodb(?:\+srv)?)://\S+`)
	reHost   = field(`host(?:name)?|server|alamat|ip|endpoint`)
	rePort   = field(`port`)
	reUser   = field(`user(?:name)?|usr|pengguna|login|uid`)
	rePass   = field(`kata\s+sandi|password|passwd|pass|pwd|pw|sandi`)
	reDBName = field(`nama\s+database|nama\s+db|basis\s+data|database|dbname|db|schema`)
	reIP     = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d{2,5})?\b`)
	reDomain = regexp.MustCompile(`(?i)\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,}(?::\d{2,5})?\b`)
	reNoSSL  = regexp.MustCompile(`(?i)(tanpa|no|non|disable|off)\W*ssl|ssl\W*(off|disable|mati|tidak)`)
	reSSL    = regexp.MustCompile(`(?i)\bssl\b|sslmode|tls`)
)

// kata yang bukan nilai (mis. "database postgres": postgres jenis, bukan nama)
var notValue = map[string]bool{"postgres": true, "postgresql": true, "mysql": true, "mariadb": true, "mongodb": true, "mongo": true,
	"sqlite": true, "sqlserver": true, "mssql": true, "pg": true, "psql": true, "saya": true, "aku": true, "kami": true, "ku": true,
	"user": true, "username": true, "password": true, "host": true, "port": true, "server": true, "di": true, "ini": true, "itu": true}

func pick(re *regexp.Regexp, text string) string {
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		v := strings.Trim(m[1], `"'`)
		if v != "" && !notValue[strings.ToLower(v)] {
			return v
		}
	}
	return ""
}

// parseDB membaca penjelasan bebas user tentang database-nya, mis.
// "postgres, host db.tokoku.com, user admin, password rahasia, database toko".
// hint: jenis database dari nama variabel (mis. MONGODB_URI) bila user tidak menyebutnya.
func parseDB(text, hint string) dbInfo {
	var d dbInfo
	for _, k := range dbKinds {
		if k.re.MatchString(text) {
			d.kind = k.kind
			break
		}
	}
	if d.kind == "" {
		d.kind = hint
	}
	d.host, d.port = pick(reHost, text), pick(rePort, text)
	d.user, d.pass, d.name = pick(reUser, text), pick(rePass, text), pick(reDBName, text)
	if d.host == "" {
		// tanpa kata "host": alamat pertama yang berbentuk IP atau domain (bukan email)
		for _, re := range []*regexp.Regexp{reIP, reDomain} {
			for _, loc := range re.FindAllStringIndex(text, -1) {
				if loc[0] > 0 && text[loc[0]-1] == '@' {
					continue
				}
				d.host = text[loc[0]:loc[1]]
				break
			}
			if d.host != "" {
				break
			}
		}
	}
	if h, p, err := net.SplitHostPort(d.host); err == nil {
		d.host = h
		if d.port == "" {
			d.port = p
		}
	}
	switch {
	case reNoSSL.MatchString(text):
		d.ssl = "disable"
	case reSSL.MatchString(text):
		d.ssl = "require"
	}
	return d
}

// connHint menebak jenis database dari nama variabel.
func connHint(key string) string {
	switch {
	case strings.Contains(key, "MONGO"):
		return "mongodb"
	case strings.Contains(key, "POSTGRES"), strings.HasPrefix(key, "PG_"):
		return "postgres"
	case strings.Contains(key, "MARIADB"):
		return "mariadb"
	case strings.Contains(key, "MYSQL"):
		return "mysql"
	case strings.Contains(key, "MSSQL"), strings.Contains(key, "SQLSERVER"):
		return "sqlserver"
	}
	return ""
}

// buildConn menyusun alamat koneksi dari penjelasan user. Bila user menempel
// alamat lengkap (postgres://...), alamat itu dipakai apa adanya. summary
// menjelaskan hasilnya tanpa password dan tanpa alamat koneksi.
func buildConn(key, text string) (conn, summary string, err error) {
	text = strings.TrimSpace(text)
	if u := reDBURL.FindString(text); u != "" {
		p, perr := url.Parse(u)
		if perr != nil || p.Host == "" {
			return "", "", fmt.Errorf("alamat database yang ditempel tidak valid")
		}
		if bad := localHost(p.Hostname()); bad != "" {
			return "", "", fmt.Errorf("%s", bad)
		}
		return u, fmt.Sprintf("database di %s (dari alamat yang ditempel)", p.Hostname()), nil
	}

	d := parseDB(text, connHint(key))
	var label, port string
	for _, k := range dbKinds {
		if k.kind == d.kind {
			label, port = k.label, k.port
		}
	}
	if d.kind == "sqlite" {
		return "", "", fmt.Errorf("SQLite berupa file di komputer sendiri, tidak bisa dihubungkan dari server; pakai database server (PostgreSQL, MySQL, ...)")
	}
	var missing []string
	if d.kind == "" {
		missing = append(missing, "jenis database (PostgreSQL, MySQL, MariaDB, SQL Server, atau MongoDB)")
	}
	if d.host == "" {
		missing = append(missing, "alamat/host database")
	}
	if d.user == "" {
		missing = append(missing, "user")
	}
	if d.pass == "" {
		missing = append(missing, "password")
	}
	if d.name == "" {
		missing = append(missing, "nama database")
	}
	if len(missing) > 0 {
		return "", "", fmt.Errorf("belum terbaca: %s", strings.Join(missing, ", "))
	}
	if bad := localHost(d.host); bad != "" {
		return "", "", fmt.Errorf("%s", bad)
	}
	portNote := ""
	if d.port == "" {
		d.port, portNote = port, " (port biasa "+label+"; sebutkan port lain bila database-mu berbeda)"
	}

	u := &url.URL{Scheme: d.kind, User: url.UserPassword(d.user, d.pass), Host: net.JoinHostPort(d.host, d.port)}
	q := url.Values{}
	switch d.kind {
	case "sqlserver":
		q.Set("database", d.name)
	default:
		u.Path = "/" + d.name
	}
	if d.ssl != "" {
		switch d.kind {
		case "postgres":
			q.Set("sslmode", d.ssl)
		case "sqlserver":
			q.Set("encrypt", map[string]string{"require": "true", "disable": "disable"}[d.ssl])
		case "mongodb":
			q.Set("tls", map[string]string{"require": "true", "disable": "false"}[d.ssl])
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), fmt.Sprintf("%s di %s port %s%s, database %s, user %s (password tersimpan)",
		label, d.host, d.port, portNote, d.name, d.user), nil
}

// localHost menolak alamat yang menunjuk ke server MCP itu sendiri: dari
// dalam container, localhost bukan komputer user.
func localHost(h string) string {
	switch strings.ToLower(h) {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1":
		return "alamat " + h + " menunjuk ke server MCP itu sendiri, bukan ke database-mu: pakai alamat/IP database yang bisa diakses dari internet"
	}
	return ""
}

// Batas panjang panduan isian.
const (
	guideLabelLen   = 80
	guideHelpLen    = 700
	guideExampleLen = 1500
)

var (
	reMultiKey = regexp.MustCompile(`CERT|_CA$|^CA_|_CA_|PEM|PRIVATE_KEY|KUBECONFIG|KUBE_CONFIG|CREDENTIALS|SERVICE_ACCOUNT|_JSON$|_YAML$`)
	reKubeKey  = regexp.MustCompile(`KUBE`)
	// port milik database user (bukan port server MCP, yang diisi otomatis)
	reDBPortKey = regexp.MustCompile(`^(?:[A-Z0-9]+_)*(?:DB|DATABASE|PG|POSTGRES|POSTGRESQL|MYSQL|MARIADB|MSSQL|SQLSERVER|MONGO|MONGODB|REDIS)_PORT$`)
	reURLKey    = regexp.MustCompile(`URL$|URI$|ENDPOINT$|BASE$`)
	reHostKey   = regexp.MustCompile(`HOST$|HOSTNAME$|SERVER$|DOMAIN$`)
	reTokenKey  = regexp.MustCompile(`TOKEN|API_KEY|APIKEY|SECRET|PASSWORD|PASSWD|_KEY$|_PAT$`)
)

// commonPorts: port yang umum dipakai layanan tujuan (bagian terakhir nama
// variabel sebelum _PORT, mis. SMTP_PORT -> SMTP).
var commonPorts = map[string]string{
	"SSH": "22", "SFTP": "22", "FTP": "21", "SMTP": "587 (atau 465 untuk SSL)", "IMAP": "993", "POP3": "995",
	"REDIS": "6379", "LDAP": "389 (atau 636 untuk LDAPS)", "MQTT": "1883", "AMQP": "5672", "RABBITMQ": "5672",
	"ELASTICSEARCH": "9200", "ES": "9200", "KAFKA": "9092", "NATS": "4222", "MEMCACHED": "11211", "HTTP": "80", "HTTPS": "443",
}

// fallbackGuide adalah panduan bawaan bila model tidak menuliskannya
// (mis. pemasangan lewat form FE): ditebak dari nama variabel.
func fallbackGuide(k string) EnvGuide {
	g := EnvGuide{Label: envLabel(k)}
	switch {
	case isConnKey(k):
		g.Help = "Ceritakan database-mu dengan kata-katamu sendiri: jenisnya (PostgreSQL, MySQL, MariaDB, SQL Server, MongoDB), " +
			"alamat/IP, port, user, password, dan nama database. Port adalah \"pintu\" database di server itu; biasanya 5432 untuk " +
			"PostgreSQL, 3306 untuk MySQL/MariaDB, 1433 untuk SQL Server, 27017 untuk MongoDB. Bila tidak disebut, port biasa itu " +
			"yang dipakai. Tulis \"pakai SSL\" bila database-nya mewajibkan. Tidak tahu? Tanyakan ke orang yang memasang " +
			"database-mu, lalu salin jawabannya ke sini."
		g.Example = "PostgreSQL, alamat db.tokoku.com, port 5432, user admin, password rahasia123, nama database toko"
		g.Multiline = true
	case reDBPortKey.MatchString(k):
		g.Label = "Port database"
		g.Help = "Nomor \"pintu\" database-mu di server itu. Biasanya 5432 untuk PostgreSQL, 3306 untuk MySQL/MariaDB, " +
			"1433 untuk SQL Server, 27017 untuk MongoDB, 6379 untuk Redis. Tanyakan ke orang yang memasang database-mu bila ragu."
		g.Example = "5432"
	case strings.HasSuffix(k, "_PORT"):
		// port layanan tujuan lain (SSH, SMTP, Redis, ...); port server MCP sendiri (PORT) diisi otomatis
		svc := strings.TrimSuffix(k, "_PORT")
		g.Label = "Port " + envLabel(svc)
		g.Help = "Nomor \"pintu\" layanan " + envLabel(svc) + " di server tujuan."
		if p, ok := commonPorts[svc[strings.LastIndex(svc, "_")+1:]]; ok {
			g.Help += " Biasanya " + p + "."
			g.Example = strings.SplitN(p, " ", 2)[0]
		}
		g.Help += " Tanyakan ke penyedia/pengelola layanan itu bila ragu."
	case reKubeKey.MatchString(k):
		g.Help = "Isi file kubeconfig (biasanya bernama config di folder .kube) yang berisi alamat server Kubernetes dan sertifikatnya. " +
			"Klik \"Unggah file\" lalu pilih filenya, atau tempel seluruh isinya. Bisa diminta dari penyedia/penanggung jawab server-mu."
		g.Example = "apiVersion: v1\nclusters:\n- cluster:\n    server: https://k8s.tokoku.com:6443\n    certificate-authority-data: LS0tLS1CRUdJTi...\n..."
		g.Multiline = true
	case reMultiKey.MatchString(k):
		g.Help = "Isi file sertifikat/kunci/kredensial yang diminta. Klik \"Unggah file\" lalu pilih filenya, atau tempel seluruh isinya."
		g.Example = "-----BEGIN CERTIFICATE-----\nMIIDdzCCAl+gAwIBAgIE...\n-----END CERTIFICATE-----"
		g.Multiline = true
	case reURLKey.MatchString(k):
		g.Help = "Alamat layanan yang ingin dihubungkan, persis seperti yang kamu pakai untuk membukanya di browser."
		g.Example = "https://layanan.tokoku.com"
	case reHostKey.MatchString(k):
		// nama server tanpa https:// (SSH, email, dan sejenisnya)
		g.Help = "Nama atau alamat IP server tujuan, tanpa http:// atau https://."
		g.Example = "server.tokoku.com"
	case reTokenKey.MatchString(k):
		g.Help = "Kunci/token akses dari halaman pengaturan (biasanya menu API, Developer, atau Integrasi) di layanan tersebut. " +
			"Perlakukan seperti password: jangan dibagikan."
		g.Example = "tempel token di sini"
	}
	return g
}

// cleanGuide merapikan panduan dari model: hanya untuk variabel yang memang
// diminta, panjang dibatasi.
func cleanGuide(keys []string, in map[string]EnvGuide) map[string]EnvGuide {
	out := map[string]EnvGuide{}
	for _, k := range keys {
		g, ok := in[k]
		if !ok {
			continue
		}
		g.Label, g.Help, g.Example = clipRunes(g.Label, guideLabelLen), clipRunes(g.Help, guideHelpLen), clipRunes(g.Example, guideExampleLen)
		if g.Label != "" || g.Help != "" || g.Example != "" {
			out[k] = g
		}
	}
	return out
}

// guideFor menggabungkan panduan tersimpan dengan panduan bawaan: bagian yang
// kosong diisi bawaan. Koneksi database selalu teks bebas (middleware yang
// menyusun alamatnya).
func guideFor(keys []string, stored map[string]EnvGuide) map[string]EnvGuide {
	out := make(map[string]EnvGuide, len(keys))
	for _, k := range keys {
		g, d := stored[k], fallbackGuide(k)
		if g.Label == "" {
			g.Label = d.Label
		}
		if g.Help == "" {
			g.Help = d.Help
		}
		if g.Example == "" {
			g.Example = d.Example
		}
		g.Multiline = g.Multiline || d.Multiline
		out[k] = g
	}
	return out
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
