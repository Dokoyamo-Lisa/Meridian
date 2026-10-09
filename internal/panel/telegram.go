package panel

// The Telegram bot. Besides the notifications (notify.go) it answers commands in the configured chat
// (/status, /servers, /traffic, /users, /risks, ...), sends a daily report, and - only when the
// operator allows changes from Telegram - lets them decide about health risks and pause or resume
// users, every change behind a confirmation button. In private chats it answers the Telegram accounts
// linked to people (tglink.go): users check their usage (/usage, /devices), the supervisor uses the
// same commands as in the configured chat, and both open their pages in the Mini App. It reads
// updates by long polling getUpdates over HTTPS (one poller, backing off on errors, stopping when the
// token is removed), answers only the configured chat (and, when set, only the allowed Telegram
// users) and linked accounts, never sends links, passwords, keys or tokens, and keeps its replies
// few and short.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// botConfig is the bot's part of the notification settings (stored with them, settings key "notify").
type botConfig struct {
	Report     bool    `json:"telegram_report,omitempty"`
	ReportHour *int    `json:"telegram_report_hour,omitempty"` // nil = 9
	Commands   bool    `json:"telegram_commands,omitempty"`
	Changes    bool    `json:"telegram_changes,omitempty"`
	Users      []int64 `json:"telegram_users,omitempty"`
	UserLink   bool    `json:"telegram_user_link,omitempty"` // users may link their Telegram accounts (tglink.go)
	Panel      bool    `json:"telegram_panel,omitempty"`     // the supervisor's linked accounts open the panel
	// HealthOffered: the "health" group was added once to a configuration from before it existed
	HealthOffered bool `json:"health_offered,omitempty"`
}

func (b botConfig) hour() int {
	if b.ReportHour == nil {
		return 9
	}
	return *b.ReportHour
}

type botView struct {
	Report     bool    `json:"telegram_report" doc:"A daily report goes to the chat: traffic, availability, servers offline, paid periods and access ending soon, data running out, open health risks"`
	ReportHour int     `json:"telegram_report_hour" doc:"The hour it is sent (0-23, the panel's time zone)"`
	Commands   bool    `json:"telegram_commands" doc:"The bot answers commands in the chat: /status, /servers, /traffic, /users, /risks, /report and more (/help lists them)"`
	Changes    bool    `json:"telegram_changes" doc:"Allow changes from Telegram: deciding about health risks with buttons, /pause and /resume of users - each only after a confirmation button, and only by the allowed users or your own linked Telegram account. Off by default"`
	Users      []int64 `json:"telegram_users" doc:"Telegram user ids allowed to use the bot in the chat; empty = everyone in the chat may read (changes need a listed user or your linked account)"`
	UserLink   bool    `json:"telegram_user_link" doc:"Users may link up to two Telegram accounts each (signing in once in the bot's Mini App, or with a code from their page) and check their usage with the bot; they may unlink one once a month. Off by default"`
	Panel      bool    `json:"telegram_panel" doc:"You may link up to two Telegram accounts of yours: they open the panel in the bot's Mini App and use the bot's commands in a private chat. A sign-in from Telegram cannot change passwords, API tokens or the site rule. Off by default"`
	MiniApp    string  `json:"telegram_mini_app" doc:"The Mini App's address; empty while the panel has no https public URL (then links are made with codes)"`
	Bot        string  `json:"telegram_bot" doc:"The bot's @username, once it is known"`
	BotStatus  string  `json:"telegram_bot_status" doc:"off, listening, or why the bot cannot read commands right now"`
	ReportSent int64   `json:"telegram_report_sent" doc:"When the last daily report went out (Unix seconds)"`
}

type botInput struct {
	Report     *bool    `json:"telegram_report" doc:"Send a daily report"`
	ReportHour *int     `json:"telegram_report_hour" doc:"Its hour, 0-23 in the panel's time zone (default 9)"`
	Commands   *bool    `json:"telegram_commands" doc:"Answer commands in the chat"`
	Changes    *bool    `json:"telegram_changes" doc:"Allow changes from Telegram (risk decisions, pausing and resuming users), each behind a confirmation button. Turned on only from a signed-in browser; an API token may turn it off"`
	Users      *[]int64 `json:"telegram_users" doc:"Telegram user ids allowed to use the bot (the numbers, not @names); empty = everyone in the chat. Changed only from a signed-in browser"`
	UserLink   *bool    `json:"telegram_user_link" doc:"Let users link their Telegram accounts and check their usage with the bot"`
	Panel      *bool    `json:"telegram_panel" doc:"Let your own linked Telegram accounts open the panel. Turned on only from a signed-in browser; an API token may turn it off"`
}

// fill brings a configuration saved before the health group existed up to date: the group is on
// by default, once - turned off later, it stays off. Someone who chose to be sent nothing still is.
func (c *notifyConfig) fill() {
	if c.HealthOffered {
		return
	}
	c.HealthOffered = true
	if len(c.Groups) > 0 && !slices.Contains(c.Groups, "health") {
		c.Groups = append(slices.Clone(c.Groups), "health")
	}
}

// applyBot takes the bot's settings from a notifications change (old: the settings before it).
// Who may change things from Telegram is set in a browser only: an API token may turn changes off,
// never on, and never change the allowed users. Another bot or chat does not inherit the right to
// change things, and removing Telegram stops the bot.
func (c *notifyConfig) applyBot(in botInput, old notifyConfig, browser bool) error {
	turnsOn := func(b *bool) bool { return b != nil && *b }
	if in.Report != nil {
		c.Report = *in.Report
	}
	if in.ReportHour != nil {
		if *in.ReportHour < 0 || *in.ReportHour > 23 {
			return errStatus(http.StatusBadRequest, "the report hour is 0 to 23")
		}
		h := *in.ReportHour
		c.ReportHour = &h
	}
	if in.Commands != nil {
		c.Commands = *in.Commands
	}
	if in.Changes != nil {
		c.Changes = *in.Changes
	}
	if in.UserLink != nil {
		c.UserLink = *in.UserLink
	}
	if in.Panel != nil {
		c.Panel = *in.Panel
	}
	if in.Users != nil {
		users := []int64{}
		for _, u := range *in.Users {
			if u <= 0 || u > 1<<52 {
				return errStatus(http.StatusBadRequest, "allowed users are Telegram user ids: positive numbers (ask @userinfobot for yours)")
			}
			if !slices.Contains(users, u) {
				users = append(users, u)
			}
		}
		if len(users) > 50 {
			return errStatus(http.StatusBadRequest, "at most 50 allowed users")
		}
		if !browser && !slices.Equal(users, old.Users) {
			return errStatus(http.StatusForbidden, "who may use the bot is set in the panel itself, signed in with your password - API tokens cannot change it")
		}
		c.Users = users
	}
	if !browser && turnsOn(in.Changes) && !old.Changes {
		return errStatus(http.StatusForbidden, "changes from Telegram can only be allowed in the panel itself, signed in with your password - API tokens cannot allow them")
	}
	if !browser && turnsOn(in.Panel) && !old.Panel {
		return errStatus(http.StatusForbidden, "opening the panel from Telegram can only be allowed in the panel itself, signed in with your password - API tokens cannot allow it")
	}
	if c.TelegramToken == "" {
		if turnsOn(in.Report) || turnsOn(in.Commands) || turnsOn(in.Changes) || turnsOn(in.UserLink) || turnsOn(in.Panel) {
			return errStatus(http.StatusBadRequest, "set up the Telegram bot token and chat first")
		}
		c.Report, c.Commands, c.Changes, c.UserLink, c.Panel = false, false, false, false, false // Telegram removed: the bot stops
	}
	if c.Changes && (c.TelegramToken != old.TelegramToken || c.TelegramChat != old.TelegramChat) && !(browser && turnsOn(in.Changes)) {
		c.Changes = false // another bot or chat: allowed again only by hand
	}
	if c.Panel && c.TelegramToken != old.TelegramToken && !(browser && turnsOn(in.Panel)) {
		c.Panel = false // another bot: its Mini App opens the panel only once allowed again by hand
	}
	return nil
}

// ---------------------------------------------------------------- state

// botState is the bot's running state.
type botState struct {
	mu      sync.Mutex
	wake    chan struct{} // the settings changed
	token   string        // the token the bot was started with
	cmds    string        // and its set of commands (commandsKey)
	name    string        // the bot's @username
	status  string
	offset  int64                  // the next update to read
	chats   []telegramChat         // chats the bot heard from lately, for "Find chats"
	pending map[string]*botPending // confirmation buttons waiting
	replies []time.Time            // when replies went out, the last minute
	quiet   bool                   // the rate limit was announced
}

// botPending is a change waiting for its confirmation button.
type botPending struct {
	kind     string // risk | ask-all | pause | resume | unlink
	linkID   int64  // unlink: the link
	riskID   int64
	decision string
	scope    string
	subID    int64
	user     int64 // only who asked may confirm
	expires  time.Time
}

func (b *botState) wakeCh() chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.wake == nil {
		b.wake = make(chan struct{}, 1)
	}
	return b.wake
}

func (b *botState) setStatus(s string) {
	b.mu.Lock()
	b.status = s
	b.mu.Unlock()
}

// botSettingsChanged wakes the poller (a removed token stops it at once) and, when the daily report
// was turned on or moved to an hour that already passed today, starts with tomorrow's.
func (p *Panel) botSettingsChanged(old, cur notifyConfig) {
	select {
	case p.bot.wakeCh() <- struct{}{}:
	default:
	}
	if cur.Report && (!old.Report || old.hour() != cur.hour()) {
		t := time.Now().In(p.loc())
		if t.Hour() >= cur.hour() {
			p.setSetting("telegram_report_day", t.Format("2006-01-02"))
		}
	}
}

func (p *Panel) botView(c notifyConfig) botView {
	sent, _ := p.getSetting("telegram_report_sent")
	p.bot.mu.Lock()
	defer p.bot.mu.Unlock()
	v := botView{Report: c.Report, ReportHour: c.hour(), Commands: c.Commands, Changes: c.Changes, Users: c.Users, Bot: p.bot.name,
		BotStatus: p.bot.status, UserLink: c.UserLink, Panel: c.Panel, MiniApp: p.miniAppURL()}
	if v.Users == nil {
		v.Users = []int64{}
	}
	if !c.listening() {
		v.BotStatus = "off"
	} else if v.BotStatus == "" {
		v.BotStatus = "starting"
	}
	v.ReportSent, _ = strconv.ParseInt(sent, 10, 64)
	return v
}

func (p *Panel) getSetting(key string) (string, bool) {
	var v string
	err := p.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	return v, err == nil
}

func (p *Panel) setSetting(key, value string) {
	_, err := p.db.Exec1(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	logErr("setting "+key, err)
}

// ---------------------------------------------------------------- Bot API

// botClient talks to Telegram with the notifications' connection settings, waiting long enough for
// a long poll.
func (p *Panel) botClient() *http.Client {
	return &http.Client{Transport: p.notify.client.Transport, Timeout: 75 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// botCall runs one Bot API method and decodes its result. Errors never carry the token.
func (p *Panel) botCall(ctx context.Context, token, method string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramAPI+"/bot"+token+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("the request to Telegram could not be made")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.botClient().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the address, which holds the token
		}
		return fmt.Errorf("cannot reach Telegram: %s", plainNetErr(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var r struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	_ = json.Unmarshal(body, &r)
	if !r.OK {
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound:
			return errors.New("the bot token was refused by Telegram - copy it again from @BotFather")
		case resp.StatusCode == http.StatusConflict:
			return errors.New("another program reads this bot's messages (a webhook or a second panel) - give Meridian a bot of its own")
		case resp.StatusCode == http.StatusTooManyRequests:
			return fmt.Errorf("too many messages for Telegram - it asks to wait %d s", r.Parameters.RetryAfter)
		case r.Description != "":
			return fmt.Errorf("refused by Telegram: %s", cleanName(r.Description, 200))
		}
		return fmt.Errorf("unexpected answer from Telegram: %s", resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return errors.New("unexpected answer from Telegram")
		}
	}
	return nil
}

type tgUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type tgChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type tgMessage struct {
	MessageID int64   `json:"message_id"`
	Date      int64   `json:"date"`
	Chat      tgChat  `json:"chat"`
	From      *tgUser `json:"from"`
	Text      string  `json:"text"`
}

type tgCallback struct {
	ID      string     `json:"id"`
	From    tgUser     `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

type tgUpdate struct {
	UpdateID int64       `json:"update_id"`
	Message  *tgMessage  `json:"message"`
	Callback *tgCallback `json:"callback_query"`
	// only noted, for "Find chats": a channel's posts, and the bot added to a group
	Post   *tgMessage `json:"channel_post"`
	Member *struct {
		Chat tgChat `json:"chat"`
	} `json:"my_chat_member"`
}

// tgButton is an inline keyboard button: a callback, or one that opens the Mini App.
type tgButton struct {
	Text   string    `json:"text"`
	Data   string    `json:"callback_data,omitempty"`
	WebApp *tgWebApp `json:"web_app,omitempty"`
}

type tgWebApp struct {
	URL string `json:"url"`
}

// listening says whether the bot reads messages: for commands in the chat, or for linked accounts.
func (c notifyConfig) listening() bool {
	return c.TelegramToken != "" && ((c.Commands && c.TelegramChat != "") || c.UserLink || c.Panel)
}

// maxMessage keeps messages below Telegram's 4096 characters.
const maxMessage = 3900

// botSend sends an HTML message to the configured chat, with buttons if any.
func (p *Panel) botSend(ctx context.Context, c notifyConfig, text string, keys [][]tgButton) error {
	return p.botSendTo(ctx, c, c.TelegramChat, text, keys)
}

// botSendTo sends an HTML message to a chat (the configured one, or a linked account's private chat).
func (p *Panel) botSendTo(ctx context.Context, c notifyConfig, chat, text string, keys [][]tgButton) error {
	form := url.Values{"chat_id": {chat}, "text": {clip(p.hideAddrs(ctx, text))}, "parse_mode": {"HTML"},
		"disable_web_page_preview": {"true"}}
	if len(keys) > 0 {
		b, _ := json.Marshal(map[string]any{"inline_keyboard": keys})
		form.Set("reply_markup", string(b))
	}
	return p.botCall(ctx, c.TelegramToken, "sendMessage", form, nil)
}

// botEdit replaces a message's text and takes its buttons away.
func (p *Panel) botEdit(ctx context.Context, c notifyConfig, msg *tgMessage, text string, keys [][]tgButton) {
	if msg == nil {
		return
	}
	form := url.Values{"chat_id": {strconv.FormatInt(msg.Chat.ID, 10)}, "message_id": {strconv.FormatInt(msg.MessageID, 10)},
		"text": {clip(p.hideAddrs(ctx, text))}, "parse_mode": {"HTML"}, "disable_web_page_preview": {"true"}}
	if len(keys) > 0 {
		b, _ := json.Marshal(map[string]any{"inline_keyboard": keys})
		form.Set("reply_markup", string(b))
	}
	if err := p.botCall(ctx, c.TelegramToken, "editMessageText", form, nil); err != nil {
		slog.Debug("telegram edit", "err", err)
	}
}

func (p *Panel) botAnswer(ctx context.Context, c notifyConfig, cb *tgCallback, text string, alert bool) {
	form := url.Values{"callback_query_id": {cb.ID}}
	if text != "" {
		form.Set("text", truncate(text, 190))
	}
	if alert {
		form.Set("show_alert", "true")
	}
	_ = p.botCall(ctx, c.TelegramToken, "answerCallbackQuery", form, nil)
}

// clip shortens a message at a line break, so no HTML tag is cut in half.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxMessage {
		return s
	}
	var b strings.Builder
	n := 0
	for _, l := range strings.Split(s, "\n") {
		k := utf8.RuneCountInString(l) + 1
		if n+k > maxMessage-2 {
			break
		}
		b.WriteString(l + "\n")
		n += k
	}
	return b.String() + "…"
}

// esc makes text from people and servers safe in Telegram's HTML.
func esc(s string) string { return html.EscapeString(s) }

var addrTokenRE = regexp.MustCompile(`[A-Za-z0-9._:-]+`)

// hideAddrs takes the servers' own addresses out of what the bot says - a chat is no place for them,
// as the status page leaves them out unless told otherwise. Other addresses (who signed in from
// where, a mining pool) stay.
func (p *Panel) hideAddrs(ctx context.Context, text string) string {
	servers, _ := p.serversOf(ctx, 0)
	own := map[string]bool{}
	for _, s := range servers {
		for _, a := range append([]string{s.Address, s.IPv4, s.IPv6}, s.Addrs...) {
			a, _, _ = strings.Cut(strings.Trim(strings.TrimSpace(a), "[]"), "/")
			if a = normAddr(a); len(a) >= 3 {
				own[a] = true
			}
		}
	}
	if len(own) == 0 {
		return text
	}
	return addrTokenRE.ReplaceAllStringFunc(text, func(tok string) string {
		core := strings.TrimRight(tok, ".:-")
		if own[normAddr(core)] {
			return "…" + tok[len(core):]
		}
		// an address with its port: 203.0.113.10:443
		if i := strings.LastIndexByte(core, ':'); i > 0 && strings.Count(core, ":") == 1 && own[normAddr(core[:i])] {
			return "…" + tok[i:]
		}
		return tok
	})
}

// normAddr writes an IP address one way (host names in lower case).
func normAddr(a string) string {
	if ip, err := netip.ParseAddr(a); err == nil {
		return ip.Unmap().String()
	}
	return strings.ToLower(a)
}

// ---------------------------------------------------------------- the poller

// telegramBot runs the bot: the daily report, and the poller while commands are on.
func (p *Panel) telegramBot(ctx context.Context) {
	go p.reportLoop(ctx)
	fails := 0
	for ctx.Err() == nil {
		c := p.notifyConfig()
		if !c.listening() {
			p.botStop(ctx, c)
			select {
			case <-ctx.Done():
				return
			case <-p.bot.wakeCh():
			}
			continue
		}
		// a poll ends early when the settings change: a removed token stops it at once
		pctx, cancel := context.WithCancel(ctx)
		changed := make(chan bool, 1)
		go func() {
			select {
			case <-p.bot.wakeCh():
				changed <- true
				cancel()
			case <-pctx.Done():
				changed <- false
			}
		}()
		err := p.pollOnce(pctx, c)
		cancel()
		if <-changed || ctx.Err() != nil {
			fails = 0
			continue
		}
		if err == nil {
			fails = 0
			p.bot.setStatus("listening")
			continue
		}
		fails++
		p.bot.setStatus(err.Error())
		slog.Warn("telegram bot", "err", err)
		wait := min(time.Duration(1<<min(fails, 9))*time.Second, 5*time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-p.bot.wakeCh():
		case <-time.After(wait):
		}
	}
}

// botStop forgets the bot when commands were turned off or the token removed, and takes its command
// list away (best effort).
func (p *Panel) botStop(ctx context.Context, c notifyConfig) {
	p.bot.mu.Lock()
	token := p.bot.token
	p.bot.token, p.bot.cmds, p.bot.name, p.bot.status, p.bot.pending, p.bot.chats = "", "", "", "off", nil, nil
	p.bot.mu.Unlock()
	if token != "" && token == c.TelegramToken {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_ = p.botCall(cctx, token, "deleteMyCommands", url.Values{}, nil)
		cancel()
	}
}

// botStart learns the bot's name and registers its commands, once per token and set of commands.
func (p *Panel) botStart(ctx context.Context, c notifyConfig) error {
	sup, _ := p.tgLinks(ctx, `l.account_id IS NOT NULL`)
	key := p.commandsKey(c, sup)
	p.bot.mu.Lock()
	same := p.bot.token == c.TelegramToken
	known := same && p.bot.cmds == key
	p.bot.mu.Unlock()
	if known {
		return nil
	}
	var me tgUser
	if err := p.botCall(ctx, c.TelegramToken, "getMe", url.Values{}, &me); err != nil {
		return err
	}
	p.registerCommands(ctx, c, sup)
	p.bot.mu.Lock()
	defer p.bot.mu.Unlock()
	if !same {
		// where this bot's updates were read up to: update numbers belong to one bot - another's
		// would skip this one's messages
		p.bot.offset, p.bot.chats = 0, nil
		if v, ok := p.getSetting("telegram_offset"); ok {
			if bot, n, ok := strings.Cut(v, ":"); ok && bot == botID(c.TelegramToken) {
				p.bot.offset, _ = strconv.ParseInt(n, 10, 64)
			}
		}
	}
	p.bot.token, p.bot.cmds, p.bot.name = c.TelegramToken, key, cleanName(me.Username, 64)
	return nil
}

// registerCommands sets the command menus: in private chats the users' commands while users may link
// their accounts (else the operator's, as in the chat), the operator's in the configured chat and in
// the supervisor's linked accounts' chats, and the menu button that opens the Mini App.
func (p *Panel) registerCommands(ctx context.Context, c notifyConfig, sup []tgLink) {
	set := func(list []tgCommand, scope any) {
		b, _ := json.Marshal(list)
		form := url.Values{"commands": {string(b)}}
		if scope != nil {
			sb, _ := json.Marshal(scope)
			form.Set("scope", string(sb))
		}
		if err := p.botCall(ctx, c.TelegramToken, "setMyCommands", form, nil); err != nil {
			slog.Warn("telegram commands", "err", err)
		}
	}
	ops := botCommands(c)
	if c.UserLink {
		set(userCommands(), nil)
		if c.Commands && c.TelegramChat != "" {
			var chat any = c.TelegramChat
			if n, err := strconv.ParseInt(c.TelegramChat, 10, 64); err == nil {
				chat = n
			}
			set(ops, map[string]any{"type": "chat", "chat_id": chat})
		}
	} else {
		set(ops, nil)
	}
	if c.Panel {
		for _, l := range sup {
			set(append(slices.Clone(ops), tgCommand{"open", "Open the panel"}, tgCommand{"unlink", "Unlink this Telegram account"}),
				map[string]any{"type": "chat", "chat_id": l.TgID})
		}
	}
	menu := map[string]any{"type": "commands"}
	if u := p.miniAppURL(); u != "" && (c.UserLink || c.Panel) {
		menu = map[string]any{"type": "web_app", "text": "Open", "web_app": map[string]string{"url": u}}
	}
	mb, _ := json.Marshal(menu)
	if err := p.botCall(ctx, c.TelegramToken, "setChatMenuButton", url.Values{"menu_button": {string(mb)}}, nil); err != nil {
		slog.Warn("telegram menu button", "err", err)
	}
}

// botCommandsChanged has the bot set its menus again (a linked account came or went).
func (p *Panel) botCommandsChanged() {
	p.bot.mu.Lock()
	p.bot.cmds = ""
	p.bot.mu.Unlock()
	select {
	case p.bot.wakeCh() <- struct{}{}:
	default:
	}
}

// botID is the bot's number: the token's part before the colon (not a secret).
func botID(token string) string {
	id, _, _ := strings.Cut(token, ":")
	return id
}

// botChats are the chats the running bot heard from lately. "Find chats" asks it while it runs with
// that token: a second reader of the bot's updates would cut the bot's own off. ok is false when it
// does not run.
func (p *Panel) botChats(token string) ([]telegramChat, bool) {
	p.bot.mu.Lock()
	defer p.bot.mu.Unlock()
	if p.bot.token == "" || p.bot.token != token {
		return nil, false
	}
	return append([]telegramChat{}, p.bot.chats...), true
}

// noteChat remembers a chat the bot heard from (the last 20).
func (p *Panel) noteChat(ch *tgChat) {
	if ch == nil || ch.ID == 0 {
		return
	}
	title := ch.Title
	if title == "" {
		title = strings.TrimSpace(ch.FirstName + " " + ch.LastName)
		if ch.Username != "" {
			title += " (@" + ch.Username + ")"
		}
	}
	c := telegramChat{ID: strconv.FormatInt(ch.ID, 10), Title: cleanName(title, 64)}
	p.bot.mu.Lock()
	defer p.bot.mu.Unlock()
	p.bot.chats = slices.DeleteFunc(p.bot.chats, func(x telegramChat) bool { return x.ID == c.ID })
	p.bot.chats = append([]telegramChat{c}, p.bot.chats...)
	if len(p.bot.chats) > 20 {
		p.bot.chats = p.bot.chats[:20]
	}
}

// commandsKey changes when the command menus do: pause and resume come with changes allowed, the
// users' commands with linking, the supervisor's private chats with their links, and the menu
// button with the Mini App's address.
func (p *Panel) commandsKey(c notifyConfig, sup []tgLink) string {
	k := fmt.Sprintf("changes=%t users=%t panel=%t chat=%s app=%s", c.Changes, c.UserLink, c.Panel, c.TelegramChat, p.miniAppURL())
	for _, l := range sup {
		k += fmt.Sprintf(" s%d", l.TgID)
	}
	return k
}

// pollOnce reads one batch of updates (waiting up to 50 seconds for them) and handles them.
func (p *Panel) pollOnce(ctx context.Context, c notifyConfig) error {
	if err := p.botStart(ctx, c); err != nil {
		return err
	}
	p.bot.mu.Lock()
	offset := p.bot.offset
	p.bot.mu.Unlock()
	var ups []tgUpdate
	form := url.Values{"timeout": {"50"}, "allowed_updates": {`["message","callback_query","channel_post","my_chat_member"]`}}
	if offset > 0 {
		form.Set("offset", strconv.FormatInt(offset, 10))
	}
	if err := p.botCall(ctx, c.TelegramToken, "getUpdates", form, &ups); err != nil {
		return err
	}
	p.bot.setStatus("listening")
	if len(ups) == 0 {
		return nil
	}
	for _, u := range ups {
		offset = max(offset, u.UpdateID+1)
		guard("telegram update", func() { p.handleUpdate(ctx, c, u) })
	}
	p.bot.mu.Lock()
	p.bot.offset = offset
	p.bot.mu.Unlock()
	p.setSetting("telegram_offset", botID(c.TelegramToken)+":"+strconv.FormatInt(offset, 10))
	return nil
}

// guard runs fn; if it panics, that is logged and the panel goes on.
func guard(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error(what+" failed", "err", r)
		}
	}()
	fn()
}

// ourChat says whether a chat is the configured one.
func ourChat(c notifyConfig, ch tgChat) bool {
	if name, ok := strings.CutPrefix(c.TelegramChat, "@"); ok {
		return ch.Username != "" && strings.EqualFold(ch.Username, name)
	}
	return strconv.FormatInt(ch.ID, 10) == c.TelegramChat
}

// allowed says whether a Telegram user may use the bot.
func allowed(c notifyConfig, u *tgUser) bool {
	if u == nil || u.IsBot {
		return false
	}
	return len(c.Users) == 0 || slices.Contains(c.Users, u.ID)
}

// whoIs names a Telegram user for the records.
func whoIs(u tgUser) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.Username != "" {
		name += " (@" + u.Username + ")"
	}
	return "Telegram: " + cleanName(nz(name, strconv.FormatInt(u.ID, 10)), 80)
}

// handleUpdate answers one message or button press - from the configured chat and allowed users
// only; everything else is ignored without a word.
func (p *Panel) handleUpdate(ctx context.Context, c notifyConfig, u tgUpdate) {
	switch {
	case u.Message != nil:
		p.noteChat(&u.Message.Chat)
	case u.Post != nil:
		p.noteChat(&u.Post.Chat)
	case u.Member != nil:
		p.noteChat(&u.Member.Chat)
	}
	switch {
	case u.Message != nil:
		m := u.Message
		if m.Chat.Type == "private" && !ourChat(c, m.Chat) {
			if m.From != nil && !m.From.IsBot && time.Now().Unix()-m.Date <= 600 {
				p.botPrivate(ctx, c, m) // a linked account, or someone who may link theirs (telegram_cmds.go)
			}
			return
		}
		if !c.Commands || !ourChat(c, m.Chat) || !allowed(c, m.From) || !strings.HasPrefix(m.Text, "/") || time.Now().Unix()-m.Date > 600 {
			return
		}
		words := strings.Fields(m.Text)
		cmd, at, _ := strings.Cut(strings.ToLower(strings.TrimPrefix(words[0], "/")), "@")
		arg := strings.Join(words[1:], " ")
		p.bot.mu.Lock()
		name := p.bot.name
		p.bot.mu.Unlock()
		if at != "" && !strings.EqualFold(at, name) {
			return // meant for another bot in the group
		}
		if !p.botAllow(ctx, c) {
			return
		}
		text, keys := p.botCommand(ctx, c, *m.From, cmd, cleanName(arg, 64))
		if err := p.botSend(ctx, c, text, keys); err != nil {
			slog.Warn("telegram reply", "err", err)
		}
	case u.Callback != nil:
		cb := u.Callback
		if cb.Message == nil {
			return
		}
		if cb.Message.Chat.Type == "private" && !ourChat(c, cb.Message.Chat) {
			if l := p.tgLinkOf(ctx, cb.From.ID); l == nil || (l.Supervisor && !c.Panel) || (!l.Supervisor && !c.UserLink) {
				p.botAnswer(ctx, c, cb, "Link your Telegram account first.", true)
				return
			}
			if !p.limiter.allow("tgpm:"+strconv.FormatInt(cb.From.ID, 10), 12, time.Minute) {
				p.botAnswer(ctx, c, cb, "Too many at once - wait a minute.", false)
				return
			}
			p.botButton(ctx, c, cb)
			return
		}
		if !c.Commands || !ourChat(c, cb.Message.Chat) {
			return
		}
		if !allowed(c, &cb.From) {
			p.botAnswer(ctx, c, cb, "You may not use this bot.", true)
			return
		}
		if !p.botAllow(ctx, c) {
			p.botAnswer(ctx, c, cb, "Too many at once - wait a minute.", false)
			return
		}
		p.botButton(ctx, c, cb)
	}
}

// botAllow limits replies to 20 a minute; the first one over says so.
func (p *Panel) botAllow(ctx context.Context, c notifyConfig) bool {
	p.bot.mu.Lock()
	t := time.Now()
	keep := p.bot.replies[:0]
	for _, at := range p.bot.replies {
		if t.Sub(at) < time.Minute {
			keep = append(keep, at)
		}
	}
	p.bot.replies = keep
	if len(keep) >= 20 {
		say := !p.bot.quiet
		p.bot.quiet = true
		p.bot.mu.Unlock()
		if say {
			_ = p.botSend(ctx, c, "Too many commands at once - wait a minute.", nil)
		}
		return false
	}
	p.bot.quiet = false
	p.bot.replies = append(p.bot.replies, t)
	p.bot.mu.Unlock()
	return true
}

// ---------------------------------------------------------------- buttons and confirmations

const changesOff = "Changes from Telegram are off. Turn on \"Allow changes from Telegram\" in the panel (Settings › Notifications)."

// riskButtons are the buttons under a risk: they only ask; the change waits for a confirmation.
func riskButtons(id int64) []tgButton {
	return []tgButton{{Text: "Acknowledge", Data: "a:" + strconv.FormatInt(id, 10)}, {Text: "Expected…", Data: "e:" + strconv.FormatInt(id, 10)}}
}

// botPend keeps a change waiting for its confirmation and returns the token its buttons carry.
func (p *Panel) botPend(x *botPending) string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	x.expires = time.Now().Add(10 * time.Minute)
	p.bot.mu.Lock()
	defer p.bot.mu.Unlock()
	if p.bot.pending == nil {
		p.bot.pending = map[string]*botPending{}
	}
	for k, v := range p.bot.pending {
		if time.Now().After(v.expires) || len(p.bot.pending) > 200 {
			delete(p.bot.pending, k)
		}
	}
	p.bot.pending[tok] = x
	return tok
}

func confirmButtons(tok, yes string) [][]tgButton {
	return [][]tgButton{{{Text: yes, Data: "c:" + tok}, {Text: "Cancel", Data: "x:" + tok}}}
}

// mayChange says whether a Telegram account may make changes: one of the allowed users, or the
// supervisor's own linked account - never "everyone in the chat".
func (p *Panel) mayChange(ctx context.Context, c notifyConfig, u tgUser) bool {
	if !c.Changes || u.IsBot {
		return false
	}
	if slices.Contains(c.Users, u.ID) {
		return true
	}
	l := p.tgLinkOf(ctx, u.ID)
	return l != nil && l.Supervisor && c.Panel
}

const notAllowed = "Only the Telegram accounts allowed in the panel (Settings › Notifications: allowed users) or your own linked account can make changes."

// botButton handles a button press.
func (p *Panel) botButton(ctx context.Context, c notifyConfig, cb *tgCallback) {
	kind, arg, _ := strings.Cut(cb.Data, ":")
	chat := strconv.FormatInt(cb.Message.Chat.ID, 10)
	if kind == "a" || kind == "e" {
		if !c.Changes {
			p.botAnswer(ctx, c, cb, changesOff, true)
			return
		}
		if !p.mayChange(ctx, c, cb.From) {
			p.botAnswer(ctx, c, cb, notAllowed, true)
			return
		}
	}
	switch kind {
	case "a", "e":
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			p.botAnswer(ctx, c, cb, "", false)
			return
		}
		v, err := p.riskByID(ctx, id)
		if err != nil {
			p.botAnswer(ctx, c, cb, "That risk is gone.", true)
			return
		}
		p.botAnswer(ctx, c, cb, "", false)
		if kind == "a" {
			tok := p.botPend(&botPending{kind: "risk", riskID: id, decision: "acknowledged", scope: "server", user: cb.From.ID})
			_ = p.botSendTo(ctx, c, chat, fmt.Sprintf("Acknowledge <b>%s</b> on %s?\nIt is flagged again if it happens again.", esc(v.Title), esc(v.Server)),
				confirmButtons(tok, "Yes, acknowledge"))
			return
		}
		here := p.botPend(&botPending{kind: "risk", riskID: id, decision: "expected", scope: "server", user: cb.From.ID})
		all := p.botPend(&botPending{kind: "ask-all", riskID: id, user: cb.From.ID})
		_ = p.botSendTo(ctx, c, chat, fmt.Sprintf("Is <b>%s</b> on %s yours? Marked as expected, it is never flagged again.", esc(v.Title), esc(v.Server)),
			[][]tgButton{{{Text: "Yes, on " + truncate(v.Server, 30), Data: "c:" + here}, {Text: "On every server…", Data: "c:" + all}},
				{{Text: "Cancel", Data: "x:" + here}}})
	case "c", "x":
		p.bot.mu.Lock()
		x := p.bot.pending[arg]
		if x != nil && x.user == cb.From.ID {
			delete(p.bot.pending, arg)
		}
		p.bot.mu.Unlock()
		switch {
		case x == nil || time.Now().After(x.expires):
			p.botAnswer(ctx, c, cb, "This question expired - ask again.", true)
			p.botEdit(ctx, c, cb.Message, "This question expired.", nil)
			return
		case x.user != cb.From.ID:
			p.botAnswer(ctx, c, cb, "Only who asked can answer this.", true)
			return
		case kind == "x":
			p.botAnswer(ctx, c, cb, "Cancelled", false)
			p.botEdit(ctx, c, cb.Message, "Cancelled - nothing changed.", nil)
			return
		case x.kind != "unlink" && !c.Changes:
			p.botAnswer(ctx, c, cb, changesOff, true)
			return
		case x.kind != "unlink" && !p.mayChange(ctx, c, cb.From):
			p.botAnswer(ctx, c, cb, notAllowed, true)
			return
		}
		p.botAnswer(ctx, c, cb, "", false)
		p.botEdit(ctx, c, cb.Message, p.botDo(ctx, c, x, cb.From), p.botFollowUp(c, x, cb.From))
	default:
		p.botAnswer(ctx, c, cb, "", false)
	}
}

// botFollowUp: "on every server" asks once more, saying what it means.
func (p *Panel) botFollowUp(c notifyConfig, x *botPending, from tgUser) [][]tgButton {
	if x.kind != "ask-all" {
		return nil
	}
	tok := p.botPend(&botPending{kind: "risk", riskID: x.riskID, decision: "expected", scope: "all", user: from.ID})
	return confirmButtons(tok, "Yes, on every server")
}

// botDo carries out a confirmed change and says what happened.
func (p *Panel) botDo(ctx context.Context, c notifyConfig, x *botPending, from tgUser) string {
	switch x.kind {
	case "ask-all":
		v, err := p.riskByID(ctx, x.riskID)
		if err != nil {
			return "That risk is gone."
		}
		return fmt.Sprintf("Mark <b>%s</b> as expected on <b>every server</b>? It is then never flagged on any server, including servers you add later.", esc(v.Title))
	case "risk":
		v, err := p.riskByID(ctx, x.riskID)
		if err != nil {
			return "That risk is gone."
		}
		if _, err := p.decideRisk(ctx, v, x.decision, x.scope, whoIs(from), 0); err != nil {
			return "Not changed: " + esc(err.Error())
		}
		where := "on " + esc(v.Server)
		if x.scope == "all" {
			where = "on every server"
		}
		if x.decision == "expected" {
			return fmt.Sprintf("✅ <b>%s</b> is expected %s - it is not flagged again.", esc(v.Title), where)
		}
		return fmt.Sprintf("✅ <b>%s</b> acknowledged %s - it is flagged again if it happens again.", esc(v.Title), where)
	case "pause", "resume":
		return p.botPause(ctx, x.subID, x.kind == "pause", whoIs(from))
	case "unlink":
		l := p.tgLinkOf(ctx, from.ID)
		if l == nil || l.ID != x.linkID {
			return "This Telegram account is not linked any more."
		}
		if err := p.unlinkTelegram(ctx, l, !l.Supervisor, whoIs(from)); err != nil {
			return "Not unlinked: " + esc(err.Error())
		}
		return "This Telegram account is unlinked: the bot and its app no longer know it. Link it again any time."
	}
	return "Nothing to do."
}

// botPause pauses or resumes a user, as the panel's button does.
func (p *Panel) botPause(ctx context.Context, subID int64, pause bool, who string) string {
	s, err := p.subByID(ctx, subID)
	if err != nil {
		return "That user is gone."
	}
	if s.Paused == pause {
		if pause {
			return esc(s.Name) + " was already paused."
		}
		return esc(s.Name) + " was not paused."
	}
	t := now()
	if pause {
		_, err = p.db.Exec1(`UPDATE subs SET paused = 1, paused_at = ?, updated_at = ? WHERE id = ?`, t, t, s.ID)
	} else {
		_, err = p.db.Exec1(`UPDATE subs SET paused = 0, paused_at = 0, updated_at = ? WHERE id = ?`, t, s.ID)
	}
	if err != nil {
		return "Not changed: the panel could not save it."
	}
	if pause {
		p.event(s.AccountID, "warn", "user_pause", 0, s.ID, 0, fmt.Sprintf("%s paused %s", who, s.Name), nil)
	} else {
		p.event(s.AccountID, "info", "user_resume", 0, s.ID, 0, fmt.Sprintf("%s resumed %s", who, s.Name), nil)
	}
	p.touchAccount(s.AccountID)
	if pause {
		return "⏸ <b>" + esc(s.Name) + "</b> is paused: their devices are disconnected until you resume them (/resume)."
	}
	return "▶️ <b>" + esc(s.Name) + "</b> is resumed: their devices can connect again."
}

// ---------------------------------------------------------------- notifications with buttons

// telegramNotify sends a batch of notifications. While commands and changes from Telegram are on,
// each health risk goes as a message of its own with the buttons to decide about it.
func (p *Panel) telegramNotify(ctx context.Context, c notifyConfig, evs []notifyEvent, text string) error {
	if !c.Commands || !c.Changes {
		return p.sendTelegram(ctx, c, text)
	}
	type riskMsg struct {
		e  notifyEvent
		id int64
	}
	var risks []riskMsg
	var rest []notifyEvent
	for _, e := range evs {
		var d struct {
			Risk int64 `json:"risk"`
		}
		if strings.HasPrefix(e.Kind, "risk_") {
			var raw string
			if p.db.QueryRowContext(ctx, `SELECT data FROM events WHERE id = ?`, e.ID).Scan(&raw) == nil &&
				json.Unmarshal([]byte(raw), &d) == nil && d.Risk > 0 && len(risks) < 10 {
				risks = append(risks, riskMsg{e, d.Risk})
				continue
			}
		}
		rest = append(rest, e)
	}
	if len(risks) == 0 {
		return p.sendTelegram(ctx, c, text)
	}
	if len(rest) > 0 {
		if err := p.sendTelegram(ctx, c, p.notifyText(rest)); err != nil {
			return err
		}
	}
	for _, r := range risks {
		mark := "🟠"
		if r.e.Level == "crit" {
			mark = "🔴"
		}
		msg := mark + " " + esc(oneLineText(r.e.Message))
		if v, err := p.riskByID(ctx, r.id); err == nil && v.Detail != "" {
			msg += "\n" + esc(oneLineText(v.Detail))
		}
		if err := p.botSend(ctx, c, msg, [][]tgButton{riskButtons(r.id)}); err != nil {
			return err
		}
	}
	return nil
}
