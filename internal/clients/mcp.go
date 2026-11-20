package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrMCPNeedsLogin berarti server MCP menolak (401/403) dan perlu login OAuth
// lewat /api/mcp/servers/:name/connect.
var ErrMCPNeedsLogin = errors.New("server MCP butuh login")

// MCPTool adalah satu tool yang disediakan server MCP.
type MCPTool struct {
	Name        string
	Title       string
	Description string
	InputSchema json.RawMessage // JSON Schema parameter tool
	ReadOnly    bool            // anotasi readOnlyHint dari server
}

// MCPCallResult adalah hasil pemanggilan tool, sudah diubah menjadi teks
// supaya bisa diberikan ke model.
type MCPCallResult struct {
	Text    string
	IsError bool
}

// MCPClient terhubung ke satu server MCP lewat Streamable HTTP. Session dibuat
// saat pertama dipakai lalu dipakai ulang; bila server mengakhirinya, session
// dibuat ulang otomatis sekali.
type MCPClient struct {
	name     string
	endpoint string
	oauth    auth.OAuthHandler // nil = tanpa OAuth (mis. memakai header statis)
	httpc    *http.Client
	client   *mcp.Client

	mu      sync.Mutex
	session *mcp.ClientSession
}

// NewMCPClient membuat klien untuk satu server. headers dikirim di setiap
// request (mis. API key server yang tidak memakai OAuth).
func NewMCPClient(name, endpoint string, headers map[string]string, oauth auth.OAuthHandler) *MCPClient {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 10 * time.Second

	var rt http.RoundTripper = tr
	if len(headers) > 0 {
		rt = &headerTransport{base: tr, headers: headers}
	}

	return &MCPClient{
		name:     name,
		endpoint: endpoint,
		oauth:    oauth,
		// tanpa Timeout keseluruhan: batas waktu diatur per panggilan lewat ctx
		httpc:  &http.Client{Transport: rt},
		client: mcp.NewClient(&mcp.Implementation{Name: "middleware-skill-ai", Version: "1.0.0"}, nil),
	}
}

// ListTools mengambil semua tool server (semua halaman).
func (c *MCPClient) ListTools(ctx context.Context) ([]MCPTool, error) {
	var tools []MCPTool
	err := c.withSession(ctx, func(s *mcp.ClientSession) error {
		tools = tools[:0]
		for t, err := range s.Tools(ctx, nil) {
			if err != nil {
				return err
			}
			schema, _ := json.Marshal(t.InputSchema)
			tool := MCPTool{Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: schema}
			if a := t.Annotations; a != nil {
				tool.ReadOnly = a.ReadOnlyHint
				if tool.Title == "" {
					tool.Title = a.Title
				}
			}
			tools = append(tools, tool)
		}
		return nil
	})
	return tools, err
}

// CallTool memanggil tool dengan argumen JSON (objek) dari model.
func (c *MCPClient) CallTool(ctx context.Context, tool string, args json.RawMessage) (*MCPCallResult, error) {
	var params any = map[string]any{}
	if len(args) > 0 && string(args) != "null" {
		var m map[string]any
		if err := json.Unmarshal(args, &m); err != nil {
			return nil, fmt.Errorf("argumen tool bukan objek JSON: %w", err)
		}
		params = m
	}

	var out *MCPCallResult
	err := c.withSession(ctx, func(s *mcp.ClientSession) error {
		res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: params})
		if err != nil {
			return err
		}
		out = &MCPCallResult{Text: resultText(res), IsError: res.IsError}
		return nil
	})
	return out, err
}

// Close menutup session (bila ada).
func (c *MCPClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		_ = c.session.Close()
		c.session = nil
	}
}

// withSession menjalankan fn dengan session aktif. Bila gagal karena session
// sudah tidak berlaku atau koneksi putus, session dibuat ulang lalu fn diulang
// sekali.
func (c *MCPClient) withSession(ctx context.Context, fn func(*mcp.ClientSession) error) error {
	for attempt := 0; attempt < 2; attempt++ {
		s, err := c.sessionFor(ctx)
		if err != nil {
			return c.wrap(err)
		}
		err = fn(s)
		if err == nil {
			return nil
		}
		if attempt == 0 && retriable(err) && ctx.Err() == nil {
			c.reset(s)
			continue
		}
		return c.wrap(err)
	}
	return nil
}

func (c *MCPClient) sessionFor(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != nil {
		return c.session, nil
	}

	t := &mcp.StreamableClientTransport{
		Endpoint:   c.endpoint,
		HTTPClient: c.httpc,
		// hanya butuh tanya-jawab; tidak perlu koneksi SSE yang terus terbuka
		DisableStandaloneSSE: true,
		OAuthHandler:         c.oauth,
	}
	s, err := c.client.Connect(ctx, t, nil)
	if err != nil {
		return nil, err
	}
	c.session = s
	return s, nil
}

// reset membuang session yang gagal (bila belum diganti goroutine lain).
func (c *MCPClient) reset(old *mcp.ClientSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == old {
		_ = old.Close()
		c.session = nil
	}
}

// wrap memberi nama server pada error dan menyeragamkan error login.
func (c *MCPClient) wrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrMCPNeedsLogin) || strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized") {
		return fmt.Errorf("%s: %w", c.name, ErrMCPNeedsLogin)
	}
	return fmt.Errorf("%s: %w", c.name, err)
}

// retriable melaporkan error yang bisa diatasi dengan membuat session baru.
func retriable(err error) bool {
	return errors.Is(err, mcp.ErrSessionMissing) || errors.Is(err, mcp.ErrConnectionClosed)
}

// resultText mengubah isi hasil tool menjadi teks. Konten non-teks (gambar,
// audio) tidak bisa dibaca model lewat pesan tool, jadi hanya disebutkan.
func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, v.Text)
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[gambar %s, %d byte, tidak ditampilkan]", v.MIMEType, len(v.Data)))
		case *mcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio %s, tidak ditampilkan]", v.MIMEType))
		case *mcp.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource: %s %s]", v.Name, v.URI))
		case *mcp.EmbeddedResource:
			if r := v.Resource; r != nil {
				if r.Text != "" {
					parts = append(parts, r.Text)
				} else {
					parts = append(parts, fmt.Sprintf("[resource %s %s]", r.URI, r.MIMEType))
				}
			}
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	return strings.Join(parts, "\n")
}

// headerTransport menambahkan header statis ke setiap request.
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}
