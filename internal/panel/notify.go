package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Notifications: what needs a person reaches the operator outside the panel - a Telegram chat
// and/or an HTTPS webhook (Slack, Discord, Mattermost, ntfy, their own). They come from the
// activity timeline: a job sends the new events of the chosen groups in order and remembers how far
// it got, so a restart neither repeats nor loses anything, and turning notifications on never sends
// the past. Nothing here pauses or changes anything: like the alerts, notifications only tell.

// notifyGroups are the kinds of events each group sends.
var notifyGroups = map[string][]string{
	"servers": {"server_offline", "server_online", "server_rebooted", "apply_failed", "core_restarted",
		"update_available", "panel_updated", "update_failed", "panel_trouble", "panel_relay_auto", "panel_relay_gone", "panel_relay_port",
		"backup_failed", "backup_restored", "ext_source_failed"},
	"users":        {"quota_reached", "node_quota_reached", "user_expired", "user_expiring", "over_ip_limit", "user_no_access"},
	"certificates": {"cert_expiring"},
	"security": {"login_failed", "login", "password_changed", "totp_disabled", "token_created", "console_opened", "telegram_panel_linked",
		"signin_blocked", "passkey_added", "passkey_removed"},
	"health": {"risk_critical", "risk_high"}, // health checks' high and critical findings (health.go)
}

var defaultNotifyGroups = []string{"servers", "users", "certificates", "health"}

// notifyConfig is stored on its own (settings key "notify"), never in the panel settings that
// read-only tokens can fetch: the bot token and a webhook URL are secrets.
type notifyConfig struct {
	TelegramToken string   `json:"telegram_token,omitempty"`
	TelegramChat  string   `json:"telegram_chat,omitempty"`
	WebhookURL    string   `json:"webhook_url,omitempty"`
	Groups        []string `json:"groups"`
	botConfig              // the Telegram bot: commands, buttons, the daily report (telegram.go)
}

func (c notifyConfig) active() bool {
	return (c.TelegramToken != "" && c.TelegramChat != "") || c.WebhookURL != ""
}

func (c notifyConfig) kinds() map[string]bool {
	out := map[string]bool{}
	for _, g := range c.Groups {
		for _, k := range notifyGroups[g] {
			out[k] = true
		}
	}
	return out
}

type notifyView struct {
	TelegramToken string   `json:"telegram_token" doc:"The bot token, masked (its bot id and last characters); empty when none is set"`
	TelegramChat  string   `json:"telegram_chat" doc:"The Telegram chat notifications go to"`
	WebhookURL    string   `json:"webhook_url" doc:"The webhook, masked (scheme and host); empty when none is set"`
	Groups        []string `json:"groups" doc:"What is sent: servers (offline, back online, rebooted, failed to apply, crashed cores, keeps losing the panel, moved to a relay, backups failed or restored, a subscription link that cannot be read), users (quota used up, a protocol's limit used up, access ended or ending, over the device limit), certificates (shared certificates, and those pasted into a protocol, expiring), security (sign-ins, failed sign-ins, password and two-factor changes, new API tokens), health (high and critical health risks)"`
	Active        bool     `json:"active" doc:"Whether a channel is set up"`
	LastSentAt    int64    `json:"last_sent_at" doc:"When a notification was last delivered (Unix seconds)"`
	LastError     string   `json:"last_error" doc:"Why the last attempt failed, if it did"`
	botView
}

type notifyInput struct {
	TelegramToken *string   `json:"telegram_token" doc:"The bot's token from @BotFather; omit to keep the current one, empty to remove it"`
	TelegramChat  *string   `json:"telegram_chat" doc:"The chat to write to: a numeric id (a group's is negative) or @channelname"`
	WebhookURL    *string   `json:"webhook_url" doc:"An HTTPS address that receives a JSON POST ({text, content, events}) per batch; omit to keep the current one, empty to remove it"`
	Groups        *[]string `json:"groups" doc:"What to send: servers, users, certificates, security, health"`
	botInput
}

type notifyTestResult struct {
	Telegram string `json:"telegram,omitempty" doc:"ok, or why it failed; empty when Telegram is not set up"`
	Webhook  string `json:"webhook,omitempty" doc:"ok, or why it failed; empty when no webhook is set up"`
}

type telegramChat struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

var (
	telegramTokenRE = regexp.MustCompile(`^\d{5,16}:[A-Za-z0-9_-]{30,64}$`)
	telegramChatRE  = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)
	telegramAPI     = "https://api.telegram.org" // tests point it at a stand-in
)

// notifier sends and remembers how the last attempt went.
type notifier struct {
	mu        sync.Mutex
	client    *http.Client
	lastSent  int64
	lastErr   string
	failSince int64     // when sending started to fail (0 = it works)
	nextTry   time.Time // backoff while it fails
	cfg       *notifyConfig
}

func newNotifier() *notifier {
	return &notifier{client: &http.Client{Timeout: 15 * time.Second,
		// a webhook that redirects could send events somewhere else, possibly in plain HTTP
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (p *Panel) notifyConfig() notifyConfig {
	p.notify.mu.Lock()
	if c := p.notify.cfg; c != nil {
		p.notify.mu.Unlock()
		return *c
	}
	p.notify.mu.Unlock()
	c := notifyConfig{Groups: defaultNotifyGroups}
	var raw string
	if p.db.QueryRow(`SELECT value FROM settings WHERE key = 'notify'`).Scan(&raw) == nil {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	c.fill()
	p.notify.mu.Lock()
	p.notify.cfg = &c
	p.notify.mu.Unlock()
	return c
}

func (p *Panel) saveNotifyConfig(c notifyConfig) error {
	b, _ := json.Marshal(c)
	if _, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('notify', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b)); err != nil {
		return err
	}
	p.notify.mu.Lock()
	p.notify.cfg = &c
	p.notify.nextTry = time.Time{}
	p.notify.mu.Unlock()
	return nil
}

func maskToken(t string) string {
	if t == "" {
		return ""
	}
	id, _, _ := strings.Cut(t, ":")
	return id + ":…" + t[len(t)-4:]
}

func maskURL(u string) string {
	if u == "" {
		return ""
	}
	pu, err := url.Parse(u)
	if err != nil {
		return "https://…"
	}
	return pu.Scheme + "://" + pu.Host + "/…"
}

func (p *Panel) viewOfNotify() notifyView {
	c := p.notifyConfig()
	p.notify.mu.Lock()
	defer p.notify.mu.Unlock()
	groups := c.Groups
	if groups == nil {
		groups = []string{}
	}
	return notifyView{TelegramToken: maskToken(c.TelegramToken), TelegramChat: c.TelegramChat, WebhookURL: maskURL(c.WebhookURL),
		Groups: groups, Active: c.active(), LastSentAt: p.notify.lastSent, LastError: p.notify.lastErr, botView: p.botView(c)}
}

// checkWebhook allows HTTPS addresses only, and none on this machine or a link-local network (a
// cloud's metadata service).
func checkWebhook(raw string) error {
	if len(raw) > 1000 {
		return errStatus(http.StatusBadRequest, "the webhook address is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errStatus(http.StatusBadRequest, "the webhook must be an https:// address (plain HTTP would send your events unencrypted)")
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return errStatus(http.StatusBadRequest, "the webhook cannot be on this machine")
	}
	if a, err := netip.ParseAddr(host); err == nil && (a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() || a.IsMulticast()) {
		return errStatus(http.StatusBadRequest, "the webhook cannot be on this machine or a link-local address")
	}
	return nil
}

func (p *Panel) apiGetNotify(w http.ResponseWriter, r *http.Request, a *Account) error {
	writeJSON(w, http.StatusOK, p.viewOfNotify())
	return nil
}

func (p *Panel) apiPutNotify(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in notifyInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c := p.notifyConfig()
	old := c
	if in.TelegramToken != nil {
		c.TelegramToken = strings.TrimSpace(*in.TelegramToken)
		if c.TelegramToken != "" && !telegramTokenRE.MatchString(c.TelegramToken) {
			return errStatus(http.StatusBadRequest, "that does not look like a bot token - @BotFather gives one like 123456789:AAE…")
		}
	}
	if in.TelegramChat != nil {
		c.TelegramChat = strings.TrimSpace(*in.TelegramChat)
		if c.TelegramChat != "" && !telegramChatRE.MatchString(c.TelegramChat) {
			return errStatus(http.StatusBadRequest, "the chat must be a numeric id (a group's starts with -) or a public @channel")
		}
	}
	if (c.TelegramToken == "") != (c.TelegramChat == "") {
		return errStatus(http.StatusBadRequest, "Telegram needs both the bot token and the chat - or neither")
	}
	if in.WebhookURL != nil {
		c.WebhookURL = strings.TrimSpace(*in.WebhookURL)
		if c.WebhookURL != "" {
			if err := checkWebhook(c.WebhookURL); err != nil {
				return err
			}
		}
	}
	if in.Groups != nil {
		groups := []string{}
		for _, g := range *in.Groups {
			if _, ok := notifyGroups[g]; !ok {
				return errStatus(http.StatusBadRequest, "groups are servers, users, certificates, security and health")
			}
			if !slices.Contains(groups, g) {
				groups = append(groups, g)
			}
		}
		c.Groups = groups
	}
	if err := c.applyBot(in.botInput, old, !authOf(r).Token); err != nil {
		return err
	}
	if err := p.saveNotifyConfig(c); err != nil {
		return err
	}
	p.botSettingsChanged(old, c)
	p.event(a.ID, "info", "settings", 0, 0, a.ID, "Notifications changed", nil)
	writeJSON(w, http.StatusOK, p.viewOfNotify())
	return nil
}

// apiTestNotify sends one message through every channel that is set up and says how each went.
func (p *Panel) apiTestNotify(w http.ResponseWriter, r *http.Request, a *Account) error {
	c := p.notifyConfig()
	if !c.active() {
		return errStatus(http.StatusBadRequest, "set up Telegram or a webhook first")
	}
	text := fmt.Sprintf("%s: a test message - notifications work.", p.settings().SiteTitle)
	ev := []notifyEvent{{TS: now(), Level: "info", Kind: "test", Message: "A test message from the panel"}}
	var res notifyTestResult
	if c.TelegramToken != "" {
		res.Telegram = "ok"
		if err := p.sendTelegram(r.Context(), c, text); err != nil {
			res.Telegram = err.Error()
		}
	}
	if c.WebhookURL != "" {
		res.Webhook = "ok"
		if err := p.sendWebhook(r.Context(), c, text, ev); err != nil {
			res.Webhook = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// apiTelegramChats lists the chats that wrote to the bot lately, so the operator can pick theirs
// instead of looking up an id: send the bot a message (or add it to a group), then ask.
func (p *Panel) apiTelegramChats(w http.ResponseWriter, r *http.Request, a *Account) error {
	var in struct {
		Token string `json:"token" doc:"The bot token; empty = the one already saved"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	token := strings.TrimSpace(in.Token)
	if token == "" {
		token = p.notifyConfig().TelegramToken
	}
	if !telegramTokenRE.MatchString(token) {
		return errStatus(http.StatusBadRequest, "paste the bot token from @BotFather first")
	}
	if list, ok := p.botChats(token); ok { // the bot reads its messages itself (telegram.go)
		writeJSON(w, http.StatusOK, list)
		return nil
	}
	body, err := p.telegramCall(r.Context(), token, "getUpdates", url.Values{"limit": {"50"}})
	if err != nil {
		return errStatus(http.StatusBadGateway, err.Error())
	}
	var resp struct {
		Result []struct {
			Message *struct {
				Chat struct {
					ID        int64  `json:"id"`
					Title     string `json:"title"`
					Username  string `json:"username"`
					FirstName string `json:"first_name"`
					LastName  string `json:"last_name"`
				} `json:"chat"`
			} `json:"message"`
			ChannelPost *struct {
				Chat struct {
					ID    int64  `json:"id"`
					Title string `json:"title"`
				} `json:"chat"`
			} `json:"channel_post"`
			MyChatMember *struct {
				Chat struct {
					ID    int64  `json:"id"`
					Title string `json:"title"`
				} `json:"chat"`
			} `json:"my_chat_member"`
		} `json:"result"`
	}
	_ = json.Unmarshal(body, &resp)
	seen := map[int64]bool{}
	out := []telegramChat{}
	add := func(id int64, title string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, telegramChat{ID: fmt.Sprint(id), Title: cleanName(title, 64)})
		}
	}
	for _, u := range resp.Result {
		switch {
		case u.Message != nil:
			c := u.Message.Chat
			title := c.Title
			if title == "" {
				title = strings.TrimSpace(c.FirstName + " " + c.LastName)
				if c.Username != "" {
					title += " (@" + c.Username + ")"
				}
			}
			add(c.ID, title)
		case u.ChannelPost != nil:
			add(u.ChannelPost.Chat.ID, u.ChannelPost.Chat.Title)
		case u.MyChatMember != nil:
			add(u.MyChatMember.Chat.ID, u.MyChatMember.Chat.Title)
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// ---------------------------------------------------------------- sending

type notifyEvent struct {
	ID      int64  `json:"-"`
	TS      int64  `json:"time"`
	Level   string `json:"level"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// telegramCall runs one Bot API method. Errors never carry the token (it is part of the URL).
func (p *Panel) telegramCall(ctx context.Context, token, method string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramAPI+"/bot"+token+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("the request to Telegram could not be made")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.notify.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the address, which holds the token
		}
		return nil, fmt.Errorf("cannot reach Telegram: %s", plainNetErr(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(body, &r)
	if !r.OK {
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusNotFound:
			return nil, errors.New("the bot token was refused by Telegram - copy it again from @BotFather")
		}
		if r.Description != "" {
			return nil, fmt.Errorf("refused by Telegram: %s", cleanName(r.Description, 200))
		}
		return nil, fmt.Errorf("unexpected answer from Telegram: %s", resp.Status)
	}
	return body, nil
}

func (p *Panel) sendTelegram(ctx context.Context, c notifyConfig, text string) error {
	if len(text) > 4000 {
		text = text[:4000] + "…"
	}
	_, err := p.telegramCall(ctx, c.TelegramToken, "sendMessage", url.Values{"chat_id": {c.TelegramChat}, "text": {text},
		"disable_web_page_preview": {"true"}})
	if err != nil && strings.Contains(err.Error(), "chat not found") {
		return errors.New("that chat is unknown to Telegram - send the bot a message first (or add it to the group), then pick the chat again")
	}
	return err
}

// sendWebhook posts {text, content, events}: "text" is what Slack and Mattermost show, "content" what
// Discord shows; "events" is for anything that reads the details.
func (p *Panel) sendWebhook(ctx context.Context, c notifyConfig, text string, evs []notifyEvent) error {
	content := text
	if len(content) > 1900 {
		content = content[:1900] + "…"
	}
	body, _ := json.Marshal(map[string]any{"text": text, "content": content, "site": p.settings().SiteTitle, "events": evs})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("the webhook address cannot be used")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Meridian/"+Version)
	resp, err := p.notify.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the address, which may hold a secret
		}
		return fmt.Errorf("the webhook cannot be reached: %s", plainNetErr(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the webhook answered %s", resp.Status)
	}
	return nil
}

// plainNetErr shortens a network error to what an operator can act on.
func plainNetErr(err error) string {
	var dns *net.DNSError
	switch {
	case errors.As(err, &dns):
		return "its name does not resolve"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout"):
		return "no answer within 15 seconds"
	}
	return cleanName(err.Error(), 200)
}

// ---------------------------------------------------------------- the job

func (p *Panel) notifyCursor() (int64, bool) {
	var v int64
	err := p.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM settings WHERE key = 'notify_cursor'`).Scan(&v)
	return v, err == nil
}

func (p *Panel) setNotifyCursor(id int64) {
	_, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES ('notify_cursor', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(id))
	logErr("notify cursor", err)
}

// notifyTick sends the events that arrived since the last tick.
func (p *Panel) notifyTick(ctx context.Context) {
	c := p.notifyConfig()
	var maxID int64
	_ = p.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM events`).Scan(&maxID)
	cursor, ok := p.notifyCursor()
	if !ok || !c.active() {
		p.setNotifyCursor(maxID) // nothing is sent while off: turning on starts from now
		return
	}
	p.notify.mu.Lock()
	wait := time.Now().Before(p.notify.nextTry)
	p.notify.mu.Unlock()
	if wait || cursor >= maxID {
		return
	}
	rows, err := p.db.QueryContext(ctx, `SELECT id, ts, level, kind, message FROM events WHERE id > ? ORDER BY id LIMIT 200`, cursor)
	if err != nil {
		slog.Error("notify", "err", err)
		return
	}
	kinds := c.kinds()
	var evs []notifyEvent
	last := cursor
	for rows.Next() {
		var e notifyEvent
		if rows.Scan(&e.ID, &e.TS, &e.Level, &e.Kind, &e.Message) == nil {
			last = e.ID
			if kinds[e.Kind] || e.Kind == "plugin_message" { // a plugin's own message is always sent
				evs = append(evs, e)
			}
		}
	}
	rows.Close()
	if len(evs) == 0 {
		p.setNotifyCursor(last)
		return
	}
	// plugins with filter:notify may change the text; an empty one holds this batch back
	text := p.plugins.filterNotify(ctx, evs, p.notifyText(evs))
	if text == "" {
		p.setNotifyCursor(last)
		return
	}
	var errs []string
	sent := false
	if c.TelegramToken != "" {
		if err := p.telegramNotify(ctx, c, evs, text); err != nil { // health risks with their buttons (telegram.go)
			errs = append(errs, err.Error())
		} else {
			sent = true
		}
	}
	if c.WebhookURL != "" {
		if err := p.sendWebhook(ctx, c, text, evs); err != nil {
			errs = append(errs, err.Error())
		} else {
			sent = true
		}
	}
	p.notify.mu.Lock()
	defer p.notify.mu.Unlock()
	if sent {
		p.notify.lastSent = now()
	}
	p.notify.lastErr = strings.Join(errs, "; ")
	if len(errs) == 0 || sent {
		p.notify.failSince, p.notify.nextTry = 0, time.Time{}
		p.setNotifyCursor(last)
		return
	}
	// every channel failed: keep the events and try again later, waiting longer each time; after a
	// day they are dropped (the panel's own timeline still has them)
	if p.notify.failSince == 0 {
		p.notify.failSince = now()
	}
	backoff := min(time.Duration(now()-p.notify.failSince+30)*time.Second, 15*time.Minute)
	p.notify.nextTry = time.Now().Add(backoff)
	if now()-p.notify.failSince > 86400 {
		slog.Warn("notifications failed for a day - dropping what waited", "err", p.notify.lastErr)
		p.notify.failSince = 0
		p.setNotifyCursor(last)
	}
}

// notifyText is one plain message for a batch of events (no markup: names are the operator's text).
func (p *Panel) notifyText(evs []notifyEvent) string {
	set := p.settings()
	loc, err := time.LoadLocation(set.Timezone)
	if err != nil {
		loc = time.UTC
	}
	var b strings.Builder
	b.WriteString(set.SiteTitle)
	if len(evs) > 1 {
		fmt.Fprintf(&b, " - %d events", len(evs))
	}
	b.WriteString("\n")
	const most = 15
	for i, e := range evs {
		if i == most {
			fmt.Fprintf(&b, "… and %d more\n", len(evs)-most)
			break
		}
		mark := "•"
		switch e.Level {
		case "crit":
			mark = "🔴"
		case "warn":
			mark = "🟠"
		}
		fmt.Fprintf(&b, "%s %s %s\n", mark, time.Unix(e.TS, 0).In(loc).Format("15:04"), oneLineText(e.Message))
	}
	if set.PublicURL != "" {
		b.WriteString(strings.TrimRight(set.PublicURL, "/") + "/overview")
	}
	return strings.TrimSpace(b.String())
}

func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

// ---------------------------------------------------------------- events from limits

// limitEvents records, once each, a user's data used up (which suspends them until it starts over:
// outofdata.go), access ended or ending soon, and shared certificates expiring - for the timeline and
// for notifications. Nothing here pauses anyone.
func (p *Panel) limitEvents(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+subCols+` FROM subs WHERE paused = 0`)
	if err != nil {
		slog.Error("limit events", "err", err)
		return
	}
	var subs []*Sub
	for rows.Next() {
		if s, err := scanSub(rows); err == nil {
			subs = append(subs, s)
		}
	}
	rows.Close()
	t := now()
	for _, s := range subs {
		p.markOut(ctx, s)      // in step with the data, whatever changed it (outofdata.go)
		p.quotaReached(ctx, s) // told already when a report used it up, unless the panel was down then
		switch {
		case s.ExpiresAt == 0:
		case t >= s.ExpiresAt && !p.eventSince(ctx, "user_expired", s.ID, s.ExpiresAt):
			p.event(s.AccountID, "warn", "user_expired", 0, s.ID, 0, fmt.Sprintf("%s's access ended on %s - nothing is paused: pause them or extend it if needed",
				s.Name, p.dateText(s.ExpiresAt)), nil)
		case t < s.ExpiresAt && s.ExpiresAt-t <= 3*86400 && !p.eventSince(ctx, "user_expiring", s.ID, s.ExpiresAt-3*86400):
			p.event(s.AccountID, "info", "user_expiring", 0, s.ID, 0, fmt.Sprintf("%s's access ends on %s", s.Name, p.dateText(s.ExpiresAt)), nil)
		}
	}
	p.nodeQuotaEvents(ctx, subs) // limits per protocol used up (nodequota.go)
	certs, err := p.db.QueryContext(ctx, `SELECT id, account_id, name, not_after FROM certs WHERE not_after > 0`)
	if err != nil {
		return
	}
	type cert struct {
		id, account, notAfter int64
		name                  string
	}
	var list []cert
	for certs.Next() {
		var c cert
		if certs.Scan(&c.id, &c.account, &c.name, &c.notAfter) == nil {
			list = append(list, c)
		}
	}
	certs.Close()
	for _, c := range list {
		if c.notAfter-t > 14*86400 {
			continue
		}
		var n int
		_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE kind = 'cert_expiring' AND ts >= ? AND CAST(json_extract(data, '$.cert') AS INTEGER) = ?`,
			c.notAfter-14*86400, c.id).Scan(&n)
		if n > 0 {
			continue
		}
		what := "expires on " + p.dateText(c.notAfter)
		if c.notAfter <= t {
			what = "has expired"
		}
		p.event(c.account, "warn", "cert_expiring", 0, 0, 0, fmt.Sprintf("The shared certificate %s %s - replace it in Settings › Certificates", c.name, what),
			map[string]int64{"cert": c.id})
	}
	p.ownCertEvents(ctx, t)
}

// ownCertEvents warns, once each, about certificates pasted into a protocol (or imported from
// certbot's files) that expire within 14 days: nothing renews them.
func (p *Panel) ownCertEvents(ctx context.Context, t int64) {
	rows, err := p.db.QueryContext(ctx, `SELECT n.id, n.kind, n.settings, n.name, s.id, s.account_id, s.name,
		CAST(json_extract(n.settings, '$.cert_expires') AS INTEGER) FROM nodes n JOIN servers s ON s.id = n.server_id
		WHERE s.deleted_at = 0 AND n.enabled = 1 AND json_extract(n.settings, '$.cert_mode') = 'custom'
		AND CAST(json_extract(n.settings, '$.cert_expires') AS INTEGER) > 0`)
	if err != nil {
		return
	}
	type own struct {
		node, server, account, notAfter int64
		label, srvName                  string
	}
	var list []own
	for rows.Next() {
		var o own
		var kind, settings, name string
		if rows.Scan(&o.node, &kind, &settings, &name, &o.server, &o.account, &o.srvName, &o.notAfter) == nil {
			o.label = nz(name, protocolLabel(kind, json.RawMessage(settings)))
			list = append(list, o)
		}
	}
	rows.Close()
	for _, o := range list {
		if o.notAfter-t > 14*86400 {
			continue
		}
		var n int
		_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE kind = 'cert_expiring' AND ts >= ? AND CAST(json_extract(data, '$.node') AS INTEGER) = ?`,
			o.notAfter-14*86400, o.node).Scan(&n)
		if n > 0 {
			continue
		}
		what := "expires on " + p.dateText(o.notAfter)
		if o.notAfter <= t {
			what = "has expired"
		}
		p.event(o.account, "warn", "cert_expiring", o.server, 0, 0, fmt.Sprintf("The certificate of %s · %s %s - nothing renews a certificate pasted into a protocol: paste the new one, or switch it to Let's Encrypt or a shared certificate",
			o.srvName, o.label, what), map[string]int64{"node": o.node})
	}
}

// dateText is a day in the panel's timezone, e.g. 7 October 2026.
func (p *Panel) dateText(ts int64) string {
	loc, err := time.LoadLocation(p.settings().Timezone)
	if err != nil {
		loc = time.UTC
	}
	return time.Unix(ts, 0).In(loc).Format("2 January 2006")
}
