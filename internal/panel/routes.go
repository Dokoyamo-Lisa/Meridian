package panel

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Handler returns the panel's HTTP handler.
func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	p.routes = nil
	handle := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, h)
		p.routes = append(p.routes, pattern)
	}

	handle("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// session
	handle("POST /api/login", p.handleLogin)
	handle("POST /api/logout", p.handleLogout)
	handle("GET /api/meta", p.apiMeta)
	handle("GET /api/me", p.authed(p.apiMe))
	handle("POST /api/me/password", p.sessionOnly(p.apiChangePassword))
	handle("POST /api/me/totp/setup", p.sessionOnly(p.apiTOTPSetup))
	handle("POST /api/me/totp/enable", p.sessionOnly(p.apiTOTPEnable))
	handle("POST /api/me/totp/disable", p.sessionOnly(p.apiTOTPDisable))
	handle("GET /api/me/sessions", p.sessionOnly(p.apiSessions))
	handle("POST /api/me/sessions/revoke-others", p.sessionOnly(p.apiRevokeOtherSessions))
	handle("GET /api/tokens", p.sessionOnly(p.apiTokens))
	handle("POST /api/tokens", p.sessionOnly(p.apiCreateToken))
	handle("DELETE /api/tokens/{id}", p.sessionOnly(p.apiDeleteToken))
	handle("GET /api/openapi.json", p.authed(p.apiOpenAPI))

	// MCP (Streamable HTTP, API token required)
	handle("POST /mcp", p.handleMCP)
	handle("GET /mcp", p.handleMCPOther)
	handle("DELETE /mcp", p.handleMCPOther)

	// overview & monitoring
	handle("GET /api/overview", p.authed(p.apiOverview))
	handle("GET /api/live", p.authed(p.apiLive))
	handle("GET /api/ips", p.authed(p.apiIPs))
	handle("GET /api/dests", p.authed(p.apiDests))
	handle("GET /api/events", p.authed(p.apiEvents))
	handle("GET /api/blocks", p.authed(p.apiBlocks))
	handle("POST /api/blocks", p.authed(p.apiCreateBlock))
	handle("DELETE /api/blocks/{id}", p.authed(p.apiDeleteBlock))

	// servers, protocols, forwards
	handle("GET /api/servers", p.authed(p.apiServers))
	handle("POST /api/servers", p.authed(p.apiCreateServer))
	handle("GET /api/servers/{id}", p.authed(p.apiServer))
	handle("PATCH /api/servers/{id}", p.authed(p.apiUpdateServer))
	handle("DELETE /api/servers/{id}", p.authed(p.apiDeleteServer))
	handle("POST /api/servers/{id}/rotate-token", p.authed(p.apiRotateServerToken))
	handle("POST /api/servers/{id}/actions", p.authed(p.apiServerAction))
	handle("GET /api/servers/{id}/metrics", p.authed(p.apiServerMetrics))
	handle("POST /api/servers/{id}/nodes", p.authed(p.apiCreateNode))
	handle("POST /api/servers/{id}/forwards", p.authed(p.apiCreateForward))
	handle("GET /api/servers/{id}/scan", p.authed(p.apiServerScan))
	handle("POST /api/servers/{id}/scan", p.authed(p.apiStartScan))
	handle("POST /api/servers/{id}/import", p.authed(p.apiImport))
	handle("GET /api/actions/{id}", p.authed(p.apiActionStatus))
	handle("PATCH /api/nodes/{id}", p.authed(p.apiUpdateNode))
	handle("POST /api/nodes/{id}/regenerate", p.authed(p.apiRegenNodeKeys))
	handle("POST /api/nodes/{id}/test-target", p.authed(p.apiTestTarget))
	handle("DELETE /api/nodes/{id}", p.authed(p.apiDeleteNode))
	handle("PATCH /api/forwards/{id}", p.authed(p.apiUpdateForward))
	handle("DELETE /api/forwards/{id}", p.authed(p.apiDeleteForward))

	// shared certificates
	handle("GET /api/certs", p.authed(p.apiCerts))
	handle("POST /api/certs", p.authed(p.apiCreateCert))
	handle("GET /api/certs/{id}", p.authed(p.apiCert))
	handle("PATCH /api/certs/{id}", p.authed(p.apiUpdateCert))
	handle("DELETE /api/certs/{id}", p.authed(p.apiDeleteCert))
	handle("GET /api/servers/{id}/config", p.authed(p.apiServerConfig))

	// users (each with a subscription link and an optional sign-in)
	handle("GET /api/users", p.authed(p.apiSubs))
	handle("POST /api/users", p.authed(p.apiCreateSub))
	handle("GET /api/users/{id}", p.authed(p.apiSub))
	handle("PATCH /api/users/{id}", p.authed(p.apiUpdateSub))
	handle("DELETE /api/users/{id}", p.authed(p.apiDeleteSub))
	handle("POST /api/users/{id}/{action}", p.authed(p.apiSubAction))
	handle("GET /api/users/{id}/ips", p.authed(p.apiSubIPs))
	handle("GET /api/users/{id}/dests", p.authed(p.apiSubDests))
	handle("GET /api/users/{id}/traffic", p.authed(p.apiSubTraffic))
	handle("GET /api/users/{id}/preview", p.authed(p.apiSubPreview))

	// protocols: what can be built and where it works
	handle("GET /api/protocols", p.authed(p.apiProtocolCatalog))
	handle("POST /api/protocols/check", p.authed(p.apiProtocolCheck))

	// the users' own pages
	handle("GET /api/portal/me", p.portal(p.apiPortalMe))
	handle("POST /api/portal/password", p.portal(p.apiPortalPassword))
	handle("POST /api/portal/logout", p.handlePortalLogout)

	// the status page: the supervisor's live dashboard
	handle("GET /api/status", p.authed(p.apiStatus))
	handle("GET /api/status/live", p.authed(p.apiStatusLive))
	handle("GET /api/places", p.authed(p.apiPlaces))

	// who may connect, by country
	handle("GET /api/access", p.authed(p.apiAccess))
	handle("PUT /api/access/servers", p.authed(p.apiPutServerRule))
	handle("PUT /api/access/site", p.sessionOnly(p.apiPutSiteRule))

	handle("GET /api/settings", p.authed(p.apiGetSettings))
	handle("PUT /api/settings", p.authed(p.apiPutSettings))
	handle("PUT /api/settings/logo", p.authed(p.apiPutLogo))
	handle("DELETE /api/settings/logo", p.authed(p.apiDeleteLogo))
	handle("GET /brand/logo", p.serveLogo)
	handle("GET /brand/icon", p.serveIcon)

	// agents
	handle("GET /agent/install.sh", p.handleInstallScript)
	handle("GET /agent/v1/state", p.handleAgentState)
	handle("POST /agent/v1/report", p.handleAgentReport)
	handle("GET /agent/v1/download/{file}", p.handleAgentDownload)
	handle("GET /agent/v1/geo/{hash}", p.handleAgentGeo)
	handle("GET /agent/v1/mirror/{core}/{version}/{asset}", p.handleMirror)

	// subscriptions (public, token in the path)
	handle("GET /s/{token}", p.handleSub)
	handle("GET /s/{token}/wg/{node}", p.handleSubWG)

	// the web app
	mux.Handle("/", p.webApp())

	p.mux = mux
	return p.securityHeaders(p.siteGate(mux))
}

func (p *Panel) securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		hd.Set("Cross-Origin-Resource-Policy", "same-origin")
		hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if p.isHTTPS(r) {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
		}
		h.ServeHTTP(w, r)
	})
}

// webApp serves the built UI with history-API fallback to index.html.
func (p *Panel) webApp() http.Handler {
	var index []byte
	if p.cfg.WebFS != nil {
		index, _ = fs.ReadFile(p.cfg.WebFS, "index.html")
	}
	if len(index) == 0 {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "the web interface is not built into this binary (build it with make)", http.StatusNotFound)
		})
	}
	panelPage := newStaticFile("index.html", index)
	var statusPage *staticFile
	statusRaw, err := fs.ReadFile(p.cfg.WebFS, "status/index.html")
	if err == nil && len(statusRaw) > 0 {
		statusPage = newStaticFile("index.html", statusRaw)
	}
	branded := &brandedPages{}
	files := &staticCache{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/agent/") {
			http.NotFound(w, r)
			return
		}
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		// hidden files (the build's .gitkeep) are never served; their paths get the page like any other
		if clean != "" && clean != "index.html" && !strings.HasPrefix(clean, ".") && !strings.Contains(clean, "/.") {
			if st, err := fs.Stat(p.cfg.WebFS, clean); err == nil && !st.IsDir() {
				f, err := files.get(p.cfg.WebFS, clean)
				if err != nil {
					http.NotFound(w, r)
					return
				}
				if strings.HasPrefix(clean, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // names carry a hash
				} else {
					w.Header().Set("Cache-Control", "no-cache") // revalidated with the ETag
				}
				f.serve(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
			"connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		page, raw, which := panelPage, index, "panel"
		if statusPage != nil && p.statusPageFor(r, "/"+clean) {
			page, raw, which = statusPage, statusRaw, "status" // the status page, or a user's own page
		}
		if l := p.logoInfo(); l.Custom || l.Animation != "assemble" {
			page = branded.get(which, l, raw) // the loading screen shows the chosen logo and animation
		}
		page.serve(w, r)
	})
}
