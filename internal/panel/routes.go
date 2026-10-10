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
	handle("POST /api/login/passkey/begin", p.handlePasskeyLoginBegin) // passkeys (passkeys.go)
	handle("POST /api/login/passkey/finish", p.handlePasskeyLoginFinish)
	handle("GET /api/me/passkeys", p.sessionOnly(p.apiPasskeys))
	handle("POST /api/me/passkeys/begin", p.sessionOnly(p.apiPasskeyBegin))
	handle("POST /api/me/passkeys/finish", p.sessionOnly(p.apiPasskeyFinish))
	handle("DELETE /api/me/passkeys/{id}", p.sessionOnly(p.apiPasskeyDelete))
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
	handle("GET /api/external-nodes", p.authed(p.apiExtNodes))
	handle("POST /api/external-nodes", p.authed(p.apiImportExt))
	handle("PATCH /api/external-nodes/{id}", p.authed(p.apiUpdateExt))
	handle("DELETE /api/external-nodes/{id}", p.authed(p.apiDeleteExt))
	handle("POST /api/external-nodes/{id}/check", p.authed(p.apiCheckExt))
	handle("GET /api/external-sources", p.authed(p.apiSources))
	handle("POST /api/external-sources", p.authed(p.apiCreateSource))
	handle("PATCH /api/external-sources/{id}", p.authed(p.apiUpdateSource))
	handle("POST /api/external-sources/{id}/refresh", p.authed(p.apiRefreshSource))
	handle("DELETE /api/external-sources/{id}", p.authed(p.apiDeleteSource))
	handle("GET /api/routing", p.authed(p.apiRouting))
	handle("POST /api/routing/rules", p.authed(p.apiCreateRoute))
	handle("PATCH /api/routing/rules/{id}", p.authed(p.apiUpdateRoute))
	handle("DELETE /api/routing/rules/{id}", p.authed(p.apiDeleteRoute))
	handle("PUT /api/routing/order", p.authed(p.apiOrderRoutes))
	handle("POST /api/routing/balancers", p.authed(p.apiCreateBalancer))
	handle("PATCH /api/routing/balancers/{id}", p.authed(p.apiUpdateBalancer))
	handle("DELETE /api/routing/balancers/{id}", p.authed(p.apiDeleteBalancer))
	handle("GET /api/risks", p.authed(p.apiRisks)) // health checks
	handle("POST /api/risks/{id}/decide", p.authed(p.apiDecideRisk))
	handle("GET /api/servers/{id}/health", p.authed(p.apiServerHealth))
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
	handle("POST /api/servers/{id}/shares", p.authed(p.apiShareServer))
	handle("DELETE /api/servers/{id}/shares", p.authed(p.apiUnshareServer))
	handle("POST /api/agents/upgrade", p.authed(p.apiUpgradeAgents))
	handle("GET /api/servers/{id}/metrics", p.authed(p.apiServerMetrics))
	handle("GET /api/servers/{id}/series", p.authed(p.apiServerSeries)) // the details' charts (pingmon.go)
	handle("GET /api/ping-monitors", p.authed(p.apiPingMonitors))
	handle("POST /api/ping-monitors", p.authed(p.apiCreatePing))
	handle("PATCH /api/ping-monitors/{id}", p.authed(p.apiUpdatePing))
	handle("DELETE /api/ping-monitors/{id}", p.authed(p.apiDeletePing))
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
	handle("GET /api/nodes/{id}/users", p.authed(p.apiNodeUsers))
	handle("PUT /api/nodes/{id}/users", p.authed(p.apiSetNodeUsers))
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
	handle("POST /api/users/{id}/plan", p.authed(p.apiApplyPlan)) // preset plans (plans.go)
	handle("GET /api/plans", p.authed(p.apiPlans))
	handle("POST /api/plans", p.authed(p.apiCreatePlan))
	handle("PATCH /api/plans/{id}", p.authed(p.apiUpdatePlan))
	handle("DELETE /api/plans/{id}", p.authed(p.apiDeletePlan))
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
	handle("GET /api/portal/telegram", p.portal(p.apiPortalTelegram)) // linked Telegram accounts (tglink.go)
	handle("POST /api/portal/telegram/code", p.portal(p.apiPortalTelegramCode))
	handle("DELETE /api/portal/telegram/{id}", p.portal(p.apiPortalTelegramUnlink))

	// the bot's Mini App: signing in from Telegram (tglink.go)
	handle("POST /api/tg/session", p.apiTgSession)
	handle("POST /api/tg/link", p.apiTgLink)
	handle("GET /api/telegram/links", p.authed(p.apiTelegramLinks))
	handle("DELETE /api/telegram/links/{id}", p.authed(p.apiTelegramUnlink))
	handle("POST /api/telegram/code", p.sessionOnly(p.apiTelegramCode))

	// the status page: the supervisor's live dashboard
	handle("GET /api/status", p.apiStatus) // the supervisor's, and everyone's while the status page is public
	handle("GET /api/status/live", p.apiStatusLive)
	handle("GET /api/status/servers/{id}/series", p.apiStatusSeries)
	handle("GET /api/places", p.authed(p.apiPlaces))

	// who may connect, by country
	handle("GET /api/access", p.authed(p.apiAccess))
	handle("PUT /api/access/servers", p.authed(p.apiPutServerRule))
	handle("PUT /api/access/site", p.sessionOnly(p.apiPutSiteRule))

	handle("GET /api/settings", p.authed(p.apiGetSettings))
	handle("PUT /api/settings", p.authed(p.apiPutSettings))
	handle("GET /api/update", p.authed(p.apiUpdate))
	handle("POST /api/update/check", p.authed(p.apiUpdateCheck))
	handle("POST /api/update/install", p.authed(p.apiUpdateInstall))
	handle("GET /api/settings/notify", p.authed(p.apiGetNotify))
	handle("PUT /api/settings/notify", p.authed(p.apiPutNotify))
	handle("POST /api/settings/notify/test", p.authed(p.apiTestNotify))
	handle("POST /api/settings/notify/telegram-chats", p.authed(p.apiTelegramChats))
	handle("POST /api/settings/notify/report", p.authed(p.apiSendReport))
	handle("GET /api/backups", p.authed(p.apiBackups)) // backups (backups.go): anything that exposes or replaces everything needs the browser
	handle("PUT /api/backups", p.sessionOnly(p.apiPutBackups))
	handle("POST /api/backups/test", p.sessionOnly(p.apiTestBackups))
	handle("POST /api/backups/run", p.authed(p.apiRunBackup))
	handle("GET /api/backups/remote", p.authed(p.apiRemoteBackups))
	handle("POST /api/backups/download", p.sessionOnly(p.apiDownloadBackup))
	handle("POST /api/backups/restore", p.sessionOnly(p.apiRestoreBackup))
	handle("DELETE /api/backups/restore", p.sessionOnly(p.apiDiscardRestore))
	handle("GET /api/settings/css", p.authed(p.apiCustomCSS)) // the operator's own styles (customcss.go)
	handle("PUT /api/settings/css", p.authed(p.apiPutCustomCSS))
	handle("GET /api/settings/turnstile", p.authed(p.apiTurnstile)) // sign-in protection (guard.go)
	handle("PUT /api/settings/turnstile", p.sessionOnly(p.apiPutTurnstile))
	handle("GET /api/settings/cloudflare", p.authed(p.apiCloudflare))
	handle("PUT /api/settings/cloudflare", p.sessionOnly(p.apiPutCloudflare))
	handle("POST /api/settings/cloudflare/test", p.sessionOnly(p.apiTestCloudflare))
	handle("GET /api/panel-address", p.authed(p.apiPanelAddress))
	handle("PUT /api/settings/logo", p.authed(p.apiPutLogo))
	handle("DELETE /api/settings/logo", p.authed(p.apiDeleteLogo))
	// plugins: anything that adds, replaces or runs code needs a signed-in browser (plugins.go)
	handle("GET /api/settings/plugins", p.authed(p.apiPlugins))
	handle("POST /api/settings/plugins", p.sessionOnly(p.apiInstallPlugin))
	handle("PUT /api/settings/plugins/{plugin}", p.sessionOnly(p.apiUpdatePlugin))
	handle("DELETE /api/settings/plugins/{plugin}", p.sessionOnly(p.apiRemovePlugin))
	handle("POST /api/settings/plugins/{plugin}/enable", p.sessionOnly(p.apiEnablePlugin))
	handle("POST /api/settings/plugins/{plugin}/disable", p.sessionOnly(p.apiDisablePlugin))
	handle("GET /api/settings/plugins/{plugin}/log", p.sessionOnly(p.apiPluginLog))
	handle("GET /brand/logo", p.serveLogo)
	handle("GET /brand/icon", p.serveIcon)

	// agents
	handle("GET /agent/install.sh", p.handleInstallScript)
	handle("GET /agent/v1/state", p.handleAgentState)
	handle("POST /agent/v1/report", p.handleAgentReport)
	handle("GET /agent/v1/download/{file}", p.handleAgentDownload)
	handle("GET /agent/v1/geo/{hash}", p.handleAgentGeo)
	handle("GET /agent/v1/ws", p.handleAgentWS)                     // the same requests on one lasting connection (agentws.go)
	handle("GET /agent/v1/console/{session}", p.handleConsoleAgent) // an agent's side of a console (console.go)
	handle("POST /api/servers/{id}/console", p.sessionOnly(p.apiOpenConsole))
	handle("GET /api/servers/{id}/console/ws", p.sessionOnly(p.apiConsoleWS))
	handle("GET /agent/v1/mirror/{core}/{version}/{asset}", p.handleMirror)

	// subscriptions (public, token in the path)
	handle("GET /s/{token}", p.handleSub)
	handle("GET /s/{token}/wg/{node}", p.handleSubWG)

	// the plugins' own API, public pages, styles and scripts (plugins_web.go)
	p.pluginRoutes(mux)
	mux.HandleFunc("GET /custom/{file}", p.handleCustomCSS) // the operator's style sheets: files, not API (customcss.go)

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
		csp := "default-src 'self'; img-src 'self' data:; font-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
			"connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
		// Cloudflare Turnstile's script and frame: on the sign-in pages while it is on, and for the
		// supervisor's own session, whose Settings check a widget before turning it on (guard.go)
		if p.turnstileOn() != "" || p.supervisorSession(r) {
			csp += "; script-src 'self' https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com"
		}
		w.Header().Set("Content-Security-Policy", csp)
		page, raw, which := panelPage, index, "panel"
		if statusPage != nil && p.statusPageFor(r, "/"+clean) {
			page, raw, which = statusPage, statusRaw, "status" // the status page, or a user's own page
		}
		if l := p.logoInfo(); l.Custom || l.Animation != "assemble" {
			page = branded.get(which, l, raw) // the loading screen shows the chosen logo and animation
		}
		page = p.plugins.page(which, page) // the styles (and status page scripts) of plugins that are on
		page = p.withCSS(which, page)      // and the operator's own styles, last (customcss.go)
		page = p.withTone(page)            // the site's own look, for people who did not pick one
		page.serve(w, r)
	})
}
