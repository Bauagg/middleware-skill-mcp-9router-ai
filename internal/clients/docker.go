package clients

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// dockerAPI adalah versi Docker Engine API yang dipakai (Docker 20.10+).
const dockerAPI = "/v1.41"

// Docker adalah klien minimal Docker Engine API untuk menjalankan server MCP
// hasil clone di container terpisah. Sengaja hanya mengurus container (buat,
// jalankan, hentikan, hapus, periksa, log); di VPS aksesnya lewat
// docker-socket-proxy yang juga hanya membuka bagian container.
type Docker struct {
	base string
	http *http.Client
}

// NewDocker membuat klien dari alamat seperti tcp://docker-proxy:2375 atau
// unix:///var/run/docker.sock.
func NewDocker(host string) (*Docker, error) {
	u, err := url.Parse(strings.TrimSpace(host))
	if err != nil {
		return nil, fmt.Errorf("alamat Docker tidak valid: %w", err)
	}
	tr := &http.Transport{ResponseHeaderTimeout: 2 * time.Minute}
	base := ""
	switch u.Scheme {
	case "tcp", "http":
		base = "http://" + u.Host
	case "unix":
		sock := u.Path
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}
		base = "http://docker"
	default:
		return nil, fmt.Errorf("alamat Docker harus tcp://host:port atau unix:///path, bukan %q", host)
	}
	return &Docker{base: base + dockerAPI, http: &http.Client{Transport: tr}}, nil
}

// ContainerSpec adalah pengaturan satu container server MCP.
type ContainerSpec struct {
	Name        string
	Image       string
	Cmd         []string
	Env         []string
	Labels      map[string]string
	Network     string
	Port        int    // port di dalam container yang dibuka (mis. 8000)
	PublishHost string // "" = tidak dipublikasikan ke host; mis. 127.0.0.1 untuk pengembangan lokal
	MemoryMB    int64
	CPUs        float64
}

// ContainerState adalah ringkasan status container.
type ContainerState struct {
	ID          string
	Running     bool
	Status      string // created | running | exited | ...
	ExitCode    int
	HostPort    string // port di host bila dipublikasikan
	NetworkName string
}

// ErrContainerNotFound berarti container tidak ada.
var ErrContainerNotFound = errors.New("container tidak ditemukan")

// Create membuat container. Container selalu tanpa hak istimewa: semua
// capability dibuang, no-new-privileges, dibatasi memori/CPU/jumlah proses,
// tanpa mount folder host, dan hanya tersambung ke network yang diberikan.
func (d *Docker) Create(ctx context.Context, s ContainerSpec) (string, error) {
	port := fmt.Sprintf("%d/tcp", s.Port)
	host := map[string]any{
		"NetworkMode":   s.Network,
		"Memory":        s.MemoryMB << 20,
		"MemorySwap":    s.MemoryMB << 20, // tanpa swap tambahan
		"NanoCpus":      int64(s.CPUs * 1e9),
		"PidsLimit":     512,
		"CapDrop":       []string{"ALL"},
		"SecurityOpt":   []string{"no-new-privileges"},
		"Privileged":    false,
		"RestartPolicy": map[string]any{"Name": "unless-stopped"},
		"LogConfig":     map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "5m", "max-file": "2"}},
	}
	if s.PublishHost != "" {
		host["PortBindings"] = map[string]any{port: []map[string]string{{"HostIp": s.PublishHost, "HostPort": ""}}}
	}
	body := map[string]any{
		"Image":        s.Image,
		"Cmd":          s.Cmd,
		"Env":          s.Env,
		"Labels":       s.Labels,
		"ExposedPorts": map[string]any{port: map[string]any{}},
		"HostConfig":   host,
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := d.do(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(s.Name), body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// Start menjalankan container.
func (d *Docker) Start(ctx context.Context, id string) error {
	return d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil)
}

// Stop menghentikan container (diberi waktu 10 detik sebelum dimatikan paksa).
func (d *Docker) Stop(ctx context.Context, id string) error {
	return d.do(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop?t=10", nil, nil)
}

// Remove menghapus container (dihentikan paksa bila masih jalan).
func (d *Docker) Remove(ctx context.Context, id string) error {
	err := d.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=1", nil, nil)
	if errors.Is(err, ErrContainerNotFound) {
		return nil
	}
	return err
}

// Inspect membaca status container.
func (d *Docker) Inspect(ctx context.Context, id string) (*ContainerState, error) {
	var out struct {
		ID    string `json:"Id"`
		State struct {
			Status   string `json:"Status"`
			Running  bool   `json:"Running"`
			ExitCode int    `json:"ExitCode"`
		} `json:"State"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := d.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, &out); err != nil {
		return nil, err
	}
	st := &ContainerState{ID: out.ID, Running: out.State.Running, Status: out.State.Status, ExitCode: out.State.ExitCode}
	for _, binds := range out.NetworkSettings.Ports {
		if len(binds) > 0 && binds[0].HostPort != "" {
			st.HostPort = binds[0].HostPort
		}
	}
	return st, nil
}

// Logs mengambil baris terakhir log container (stdout + stderr).
func (d *Docker) Logs(ctx context.Context, id string, tail int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/containers/%s/logs?stdout=1&stderr=1&tail=%d", d.base, url.PathEscape(id), tail), nil)
	if err != nil {
		return "", err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("Docker tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("Docker menjawab %d: %s", resp.StatusCode, snippet(raw))
	}
	return demuxLogs(raw), nil
}

// demuxLogs membuang header 8 byte per potongan pada log container tanpa TTY.
func demuxLogs(raw []byte) string {
	var b strings.Builder
	for len(raw) >= 8 && (raw[0] == 1 || raw[0] == 2) && raw[1] == 0 && raw[2] == 0 && raw[3] == 0 {
		n := int(binary.BigEndian.Uint32(raw[4:8]))
		if 8+n > len(raw) {
			n = len(raw) - 8
		}
		b.Write(raw[8 : 8+n])
		raw = raw[8+n:]
	}
	b.Write(raw)
	return b.String()
}

func (d *Docker) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return fmt.Errorf("Docker tidak dapat dihubungi: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrContainerNotFound
	case resp.StatusCode == http.StatusNotModified:
		return nil // sudah jalan / sudah berhenti
	case resp.StatusCode/100 != 2:
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Message != "" {
			return fmt.Errorf("Docker menjawab %d: %s", resp.StatusCode, e.Message)
		}
		return fmt.Errorf("Docker menjawab %d: %s", resp.StatusCode, snippet(raw))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}
