package clients

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// MCPAuthInfo adalah hasil discovery OAuth sebuah server MCP: di mana login,
// di mana menukar token, dan bagaimana mendaftarkan klien.
type MCPAuthInfo struct {
	Resource        string   // resource server (dikirim sebagai parameter "resource")
	Scopes          []string // scope yang diminta saat login
	AuthURL         string
	TokenURL        string
	RegistrationURL string   // kosong = server tidak mendukung pendaftaran otomatis
	AuthMethods     []string // token_endpoint_auth_methods_supported
}

// MCPClientRegistration adalah hasil pendaftaran klien OAuth (DCR).
type MCPClientRegistration struct {
	ClientID     string
	ClientSecret string
	AuthMethod   string // none, client_secret_post, atau client_secret_basic
}

var authHTTP = &http.Client{Timeout: 20 * time.Second}

// DiscoverMCPAuth mencari metadata OAuth server MCP sesuai spesifikasi MCP:
// protected resource metadata (RFC 9728) -> authorization server metadata
// (RFC 8414, atau OpenID Connect discovery).
func DiscoverMCPAuth(ctx context.Context, endpoint string) (*MCPAuthInfo, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("url server MCP tidak valid: %w", err)
	}
	origin := u.Scheme + "://" + u.Host
	path := strings.TrimRight(u.Path, "/")

	// 1. protected resource metadata: versi dengan path dulu, lalu di root
	var prm *oauthex.ProtectedResourceMetadata
	candidates := []struct{ meta, resource string }{
		{origin + "/.well-known/oauth-protected-resource" + path, origin + path},
		{origin + "/.well-known/oauth-protected-resource", origin},
		{origin + "/.well-known/oauth-protected-resource", origin + path},
	}
	var lastErr error
	for _, c := range candidates {
		prm, lastErr = oauthex.GetProtectedResourceMetadata(ctx, c.meta, c.resource, authHTTP)
		if lastErr == nil && prm != nil && len(prm.AuthorizationServers) > 0 {
			break
		}
		prm = nil
	}
	if prm == nil {
		if lastErr == nil {
			lastErr = errors.New("metadata tidak ditemukan")
		}
		return nil, fmt.Errorf("server tidak menyediakan metadata OAuth MCP: %w", lastErr)
	}

	// 2. authorization server metadata
	issuer := strings.TrimRight(prm.AuthorizationServers[0], "/")
	asm, err := authServerMeta(ctx, issuer)
	if err != nil {
		return nil, err
	}

	return &MCPAuthInfo{
		Resource:        prm.Resource,
		Scopes:          prm.ScopesSupported,
		AuthURL:         asm.AuthorizationEndpoint,
		TokenURL:        asm.TokenEndpoint,
		RegistrationURL: asm.RegistrationEndpoint,
		AuthMethods:     asm.TokenEndpointAuthMethodsSupported,
	}, nil
}

// authServerMeta mencoba lokasi metadata yang umum: RFC 8414 (dengan path
// issuer disisipkan) lalu OpenID Connect discovery.
func authServerMeta(ctx context.Context, issuer string) (*oauthex.AuthServerMeta, error) {
	iu, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("issuer tidak valid: %w", err)
	}
	origin, path := iu.Scheme+"://"+iu.Host, strings.TrimRight(iu.Path, "/")

	urls := []string{
		origin + "/.well-known/oauth-authorization-server" + path,
		origin + "/.well-known/openid-configuration" + path,
		issuer + "/.well-known/openid-configuration",
	}
	var lastErr error
	for _, mu := range urls {
		meta, err := oauthex.GetAuthServerMeta(ctx, mu, issuer, authHTTP)
		if err == nil && meta != nil {
			return meta, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("metadata tidak ditemukan")
	}
	return nil, fmt.Errorf("metadata authorization server %s: %w", issuer, lastErr)
}

// RegisterMCPClient mendaftarkan middleware sebagai klien OAuth (Dynamic
// Client Registration, RFC 7591). Klien publik ("none" + PKCE) dipilih bila
// didukung; bila tidak, memakai client secret.
func RegisterMCPClient(ctx context.Context, info *MCPAuthInfo, redirectURI, clientName string) (*MCPClientRegistration, error) {
	if info.RegistrationURL == "" {
		return nil, errors.New("server tidak mendukung pendaftaran klien otomatis (DCR)")
	}

	method := pickAuthMethod(info.AuthMethods)
	meta := &oauthex.ClientRegistrationMetadata{
		ClientName:              clientName,
		RedirectURIs:            []string{redirectURI},
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: method,
		Scope:                   strings.Join(info.Scopes, " "),
	}
	res, err := oauthex.RegisterClient(ctx, info.RegistrationURL, meta, authHTTP)
	if err != nil {
		return nil, fmt.Errorf("pendaftaran klien OAuth gagal: %w", err)
	}
	if res.TokenEndpointAuthMethod != "" {
		method = res.TokenEndpointAuthMethod
	}
	return &MCPClientRegistration{ClientID: res.ClientID, ClientSecret: res.ClientSecret, AuthMethod: method}, nil
}

func pickAuthMethod(supported []string) string {
	if len(supported) == 0 {
		return "client_secret_basic" // bawaan RFC 8414
	}
	for _, want := range []string{"none", "client_secret_post", "client_secret_basic"} {
		for _, s := range supported {
			if s == want {
				return want
			}
		}
	}
	return supported[0]
}
