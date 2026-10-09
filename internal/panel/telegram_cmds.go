package panel

// What the Telegram bot says: its commands and the daily report. Messages are short, in plain
// English, in Telegram's HTML with every name escaped - and never carry links, passwords, keys,
// tokens or server addresses.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"meridian/internal/proto"
)

type tgCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// botCommands is the bot's command list, as Telegram shows it.
func botCommands(c notifyConfig) []tgCommand {
	list := []tgCommand{
		{"status", "Servers, users and traffic now, and what needs you"},
		{"servers", "Every server: up or down, users, throughput, load"},
		{"server", "One server in detail: /server name"},
		{"traffic", "Traffic per server: /traffic today or /traffic month"},
		{"users", "Users with their usage and limits"},
		{"user", "One user in detail: /user name"},
		{"online", "Who is connected now"},
		{"top", "Users with the most traffic today and this month"},
		{"events", "The latest events"},
		{"risks", "Open health risks"},
		{"ping", "Ping monitors: the last round trips from each server"},
		{"expiring", "Paid periods and access ending soon, data running out"},
		{"report", "The daily report, now"},
		{"help", "What this bot does"},
	}
	if c.Changes {
		list = append(list, tgCommand{"pause", "Pause a user: /pause name"}, tgCommand{"resume", "Resume a user: /resume name"})
	}
	return list
}

// botCommand answers one command.
func (p *Panel) botCommand(ctx context.Context, c notifyConfig, from tgUser, cmd, arg string) (string, [][]tgButton) {
	switch cmd {
	case "start", "help":
		return p.botHelp(c), nil
	case "status":
		return p.botStatus(ctx), nil
	case "servers":
		return p.botServers(ctx), nil
	case "server":
		return p.botServer(ctx, arg), nil
	case "traffic":
		return p.botTraffic(ctx, arg), nil
	case "users":
		return p.botUsers(ctx), nil
	case "user":
		return p.botUser(ctx, arg), nil
	case "online":
		return p.botOnline(ctx), nil
	case "top":
		return p.botTop(ctx), nil
	case "events":
		return p.botEvents(ctx), nil
	case "risks":
		return p.botRisks(ctx, c)
	case "expiring":
		return p.botExpiring(ctx, 7), nil
	case "ping":
		return p.botPing(ctx), nil
	case "report":
		return p.dailyReport(ctx), nil
	case "pause", "resume":
		if !c.Changes {
			return changesOff, nil
		}
		if !p.mayChange(ctx, c, from) {
			return notAllowed, nil
		}
		s, msg := p.botFindUser(ctx, arg)
		if s == nil {
			return msg, nil
		}
		if s.Paused == (cmd == "pause") {
			return esc(s.Name) + map[bool]string{true: " is already paused.", false: " is not paused."}[s.Paused], nil
		}
		tok := p.botPend(&botPending{kind: cmd, subID: s.ID, user: from.ID})
		if cmd == "pause" {
			return fmt.Sprintf("Pause <b>%s</b>? Every device of theirs is disconnected now, until you resume them.", esc(s.Name)),
				confirmButtons(tok, "Yes, pause "+truncate(s.Name, 30))
		}
		return fmt.Sprintf("Resume <b>%s</b>? Their devices can connect again.", esc(s.Name)), confirmButtons(tok, "Yes, resume "+truncate(s.Name, 30))
	}
	return "I don't know that command. /help lists what I can do.", nil
}

func (p *Panel) botHelp(c notifyConfig) string {
	var b strings.Builder
	b.WriteString("<b>" + esc(p.settings().SiteTitle) + "</b> - what I can tell you:\n")
	for _, x := range botCommands(c) {
		if x.Command == "help" {
			continue
		}
		fmt.Fprintf(&b, "/%s - %s\n", x.Command, esc(x.Description))
	}
	if c.Changes {
		b.WriteString("\nUnder health risks, buttons let you mark one as expected or acknowledge it. Every change asks you to confirm first.")
	} else {
		b.WriteString("\nI only tell; changes from Telegram are off.")
	}
	return b.String()
}

// ---------------------------------------------------------------- what is running

// botNow is what the commands about now share: servers, users and who is connected.
type botNow struct {
	servers []*Server
	subs    []*Sub
	online  map[int64][]onlineIP
}

func (p *Panel) botNowData(ctx context.Context) botNow {
	var n botNow
	n.servers, _ = p.serversOf(ctx, 0)
	n.subs, _ = p.subsOf(ctx, 0)
	n.online = p.onlineBySub(ctx, 0)
	return n
}

func fmtRate(bytesPerSec int64) string {
	b := float64(bytesPerSec) * 8
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.1f Gbit/s", b/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.1f Mbit/s", b/1e6)
	case b >= 1e3:
		return fmt.Sprintf("%.0f kbit/s", b/1e3)
	}
	return fmt.Sprintf("%.0f bit/s", b)
}

func (p *Panel) nowText() string {
	return time.Now().In(p.loc()).Format("2 Jan 2006, 15:04")
}

func (p *Panel) botStatus(ctx context.Context) string {
	n := p.botNowData(ctx)
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b> · %s\n", esc(p.settings().SiteTitle), p.nowText())
	up, pending := 0, 0
	var down []string
	var rx, tx int64
	for _, s := range n.servers {
		switch {
		case s.FirstSeenAt == 0:
			pending++
		case s.Online:
			up++
			if ls := p.live.get(s.ID); ls != nil {
				rx, tx = rx+ls.Live.Sys.RXRate, tx+ls.Live.Sys.TXRate
			}
		default:
			down = append(down, esc(s.Name))
		}
	}
	fmt.Fprintf(&b, "Servers: %d of %d online", up, len(n.servers)-pending)
	if len(down) > 0 {
		fmt.Fprintf(&b, " · offline: %s", strings.Join(down, ", "))
	}
	b.WriteString("\n")
	users, devices := 0, 0
	for _, list := range n.online {
		if len(list) > 0 {
			users++
			devices += distinctIPs(list)
		}
	}
	fmt.Fprintf(&b, "Users: %d · %d online now on %d device(s)\n", len(n.subs), users, devices)
	fmt.Fprintf(&b, "Now: ↓ %s · ↑ %s\n", fmtRate(rx), fmtRate(tx))
	if days := p.accountDays(ctx, 0, 1); len(days) == 1 {
		fmt.Fprintf(&b, "Users' traffic today: %s\n", fmtBytes(days[0].Up+days[0].Down))
	}
	alerts := p.alerts(ctx, 0, n.servers, n.subs, n.online)
	if len(alerts) == 0 {
		b.WriteString("\n🟢 Nothing needs you right now.")
	} else {
		fmt.Fprintf(&b, "\n<b>Needs you (%d)</b>\n", len(alerts))
		for i, a := range alerts {
			if i == 8 {
				fmt.Fprintf(&b, "… and %d more\n", len(alerts)-8)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", levelMark(a.Level), esc(a.Message))
		}
	}
	return strings.TrimSpace(b.String())
}

func levelMark(level string) string {
	switch level {
	case "crit":
		return "🔴"
	case "warn":
		return "🟠"
	}
	return "•"
}

func (p *Panel) botServers(ctx context.Context) string {
	n := p.botNowData(ctx)
	if len(n.servers) == 0 {
		return "No servers yet."
	}
	var b strings.Builder
	b.WriteString("<b>Servers</b>\n")
	for _, s := range n.servers {
		switch {
		case s.FirstSeenAt == 0:
			fmt.Fprintf(&b, "⚪ %s - waiting for its agent\n", esc(s.Name))
		case !s.Online:
			fmt.Fprintf(&b, "🔴 %s - offline for %s\n", esc(s.Name), humanDuration(now()-s.StatusChangedAt))
		default:
			line := "🟢 " + esc(s.Name)
			if ls := p.live.get(s.ID); ls != nil {
				line += fmt.Sprintf(" - %d online · ↑ %s · CPU %.0f%%", liveIPs(ls.Live.Online), fmtRate(ls.Live.Sys.TXRate), ls.Live.Sys.CPU)
			}
			b.WriteString(line + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// findByName finds a server or user by name: exactly, else the only one whose name starts with, or holds, q.
func findByName[T any](list []T, name func(T) string, q string) (T, int) {
	var zero T
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return zero, 0
	}
	var starts, holds []T
	for _, x := range list {
		n := strings.ToLower(name(x))
		switch {
		case n == q:
			return x, 1
		case strings.HasPrefix(n, q):
			starts = append(starts, x)
		case strings.Contains(n, q):
			holds = append(holds, x)
		}
	}
	if len(starts) == 1 {
		return starts[0], 1
	}
	if len(starts) > 1 {
		return zero, len(starts)
	}
	if len(holds) == 1 {
		return holds[0], 1
	}
	return zero, len(holds)
}

func (p *Panel) botServer(ctx context.Context, q string) string {
	servers, _ := p.serversOf(ctx, 0)
	s, n := findByName(servers, func(s *Server) string { return s.Name }, q)
	switch {
	case q == "":
		return "Which server? /server name - /servers lists them."
	case n == 0:
		return "No server is called " + esc(q) + ". /servers lists them."
	case n > 1:
		return fmt.Sprintf("%d servers match %s - type more of the name.", n, esc(q))
	}
	var b strings.Builder
	status := "🟢 online"
	switch {
	case s.FirstSeenAt == 0:
		status = "⚪ waiting for its agent"
	case !s.Online:
		status = "🔴 offline for " + humanDuration(now()-s.StatusChangedAt)
	}
	fmt.Fprintf(&b, "<b>%s</b> %s\n", esc(s.Name), status)
	if place := strings.Trim(s.City+", "+s.Country, ", "); place != "" {
		b.WriteString(esc(place) + "\n")
	}
	if ls := p.live.get(s.ID); ls != nil && s.Online {
		sys := ls.Live.Sys
		mem, disk := 0.0, 0.0
		if sys.MemTotal > 0 {
			mem = float64(sys.MemUsed) * 100 / float64(sys.MemTotal)
		}
		if sys.DiskTotal > 0 {
			disk = float64(sys.DiskUsed) * 100 / float64(sys.DiskTotal)
		}
		fmt.Fprintf(&b, "CPU %.0f%% · load %.2f · memory %.0f%% · disk %.0f%%\n", sys.CPU, sys.Load1, mem, disk)
		users := map[int64]bool{}
		for _, u := range ls.Live.Online {
			users[u.Sub] = true
		}
		fmt.Fprintf(&b, "Now: ↓ %s · ↑ %s · %d user(s) on %d device(s)\n", fmtRate(sys.RXRate), fmtRate(sys.TXRate), len(users), liveIPs(ls.Live.Online))
		fmt.Fprintf(&b, "Up for %s · agent %s\n", humanDuration(sys.Uptime), esc(nz(s.AgentVersion, "?")))
	}
	today, month := p.serverTraffic(ctx, []int64{s.ID})
	fmt.Fprintf(&b, "Traffic: today %s · this month %s\n", fmtBytes(today[s.ID]), fmtBytes(month[s.ID]))
	if s.BwLimit > 0 {
		fmt.Fprintf(&b, "Plan: %s of %s used (%.0f%%), resets on day %d\n", fmtBytes(s.BwUsed()), fmtBytes(s.BwLimit),
			float64(s.BwUsed())*100/float64(s.BwLimit), max(s.BwResetDay, 1))
	}
	if s.ExpiresOn != "" {
		fmt.Fprintf(&b, "Paid until %s\n", esc(s.ExpiresOn))
	}
	if av := p.availabilityOf(ctx, []int64{s.ID}, nil)[s.ID]; av != nil && av.H24 != nil {
		line := fmt.Sprintf("Available: %.2f%% (24 h)", *av.H24)
		if av.D30 != nil {
			line += fmt.Sprintf(" · %.2f%% (30 days)", *av.D30)
		}
		b.WriteString(line + "\n")
	}
	if risks, _ := p.risks(ctx, riskFilter{server: s.ID, status: "open", limit: 100}); len(risks) > 0 {
		fmt.Fprintf(&b, "Health: %d open risk(s), the worst %s - /risks\n", len(risks), risks[0].Severity)
	}
	return strings.TrimSpace(b.String())
}

// serverTraffic is what servers sent and received today and this month (calendar month, panel time).
func (p *Panel) serverTraffic(ctx context.Context, ids []int64) (today, month map[int64]int64) {
	today, month = map[int64]int64{}, map[int64]int64{}
	t := time.Now().In(p.loc())
	day, first := t.Format("2006-01-02"), t.Format("2006-01")+"-01"
	rows, err := p.db.QueryContext(ctx, `SELECT server_id, day, rx + tx FROM server_daily WHERE day >= ?`, first)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, n int64
		var d string
		if rows.Scan(&id, &d, &n) != nil || (len(ids) > 0 && !slices.Contains(ids, id)) {
			continue
		}
		month[id] += n
		if d == day {
			today[id] += n
		}
	}
	return
}

// userTraffic is each user's traffic since a day (panel time).
func (p *Panel) userTraffic(ctx context.Context, since string) map[int64]int64 {
	out := map[int64]int64{}
	rows, err := p.db.QueryContext(ctx, `SELECT sub_id, SUM(up + down) FROM traffic_daily WHERE day >= ? GROUP BY sub_id`, since)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, n int64
		if rows.Scan(&id, &n) == nil {
			out[id] = n
		}
	}
	return out
}

func (p *Panel) botTraffic(ctx context.Context, arg string) string {
	month := strings.HasPrefix(strings.ToLower(arg), "m")
	servers, _ := p.serversOf(ctx, 0)
	td, md := p.serverTraffic(ctx, nil)
	use := td
	what := "today"
	since := time.Now().In(p.loc()).Format("2006-01-02")
	if month {
		use, what = md, "this month"
		since = time.Now().In(p.loc()).Format("2006-01") + "-01"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Traffic %s</b> (in and out of each server)\n", what)
	var total int64
	sort.SliceStable(servers, func(i, j int) bool { return use[servers[i].ID] > use[servers[j].ID] })
	for _, s := range servers {
		total += use[s.ID]
		line := fmt.Sprintf("• %s - %s", esc(s.Name), fmtBytes(use[s.ID]))
		if month && s.BwLimit > 0 {
			line += fmt.Sprintf(" · plan %.0f%% used", float64(s.BwUsed())*100/float64(s.BwLimit))
		}
		b.WriteString(line + "\n")
	}
	var users int64
	for _, n := range p.userTraffic(ctx, since) {
		users += n
	}
	fmt.Fprintf(&b, "All servers: %s · users' traffic: %s", fmtBytes(total), fmtBytes(users))
	if !month {
		b.WriteString("\n/traffic month for this month")
	}
	return b.String()
}

// ---------------------------------------------------------------- users

func (p *Panel) botFindUser(ctx context.Context, q string) (*Sub, string) {
	subs, _ := p.subsOf(ctx, 0)
	s, n := findByName(subs, func(s *Sub) string { return s.Name }, q)
	switch {
	case q == "":
		return nil, "Which user? Add the name - /users lists them."
	case n == 0:
		return nil, "No user is called " + esc(q) + ". /users lists them."
	case n > 1:
		return nil, fmt.Sprintf("%d users match %s - type more of the name.", n, esc(q))
	}
	return s, ""
}

// usedOf says how much of their data a user used this cycle.
func usedOf(s *Sub) string {
	if s.Quota > 0 {
		return fmt.Sprintf("%s of %s", fmtBytes(s.Used()), fmtBytes(s.Quota))
	}
	return fmtBytes(s.Used())
}

func (p *Panel) botUsers(ctx context.Context) string {
	n := p.botNowData(ctx)
	if len(n.subs) == 0 {
		return "No users yet."
	}
	subs := slices.Clone(n.subs)
	sort.SliceStable(subs, func(i, j int) bool { return subs[i].Used() > subs[j].Used() })
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Users</b> (%d), by data used this cycle\n", len(subs))
	for i, s := range subs {
		if i == 40 {
			fmt.Fprintf(&b, "… and %d more - /user name for one\n", len(subs)-40)
			break
		}
		mark := "•"
		switch {
		case s.Paused:
			mark = "⏸"
		case len(n.online[s.ID]) > 0:
			mark = "🟢"
		}
		line := fmt.Sprintf("%s %s - %s", mark, esc(s.Name), usedOf(s))
		if s.ExpiresAt > 0 {
			line += " · until " + time.Unix(s.ExpiresAt, 0).In(p.loc()).Format("2 Jan")
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSpace(b.String())
}

func (p *Panel) botUser(ctx context.Context, q string) string {
	s, msg := p.botFindUser(ctx, q)
	if s == nil {
		return msg
	}
	var b strings.Builder
	status := "active"
	if s.Paused {
		status = "⏸ paused"
	}
	fmt.Fprintf(&b, "<b>%s</b> - %s\n", esc(s.Name), status)
	fmt.Fprintf(&b, "This cycle: %s (↓ %s · ↑ %s)", usedOf(s), fmtBytes(s.CycleDown), fmtBytes(s.CycleUp))
	if s.ResetDay > 0 {
		fmt.Fprintf(&b, ", resets on day %d", s.ResetDay)
	}
	fmt.Fprintf(&b, "\nAll time: %s\n", fmtBytes(s.TotalUp+s.TotalDown))
	if s.ExpiresAt > 0 {
		word := "ends"
		if s.ExpiresAt <= now() {
			word = "ended"
		}
		fmt.Fprintf(&b, "Access %s on %s\n", word, p.dateText(s.ExpiresAt))
	}
	online := p.onlineBySub(ctx, 0)[s.ID]
	devices := distinctIPs(online)
	line := fmt.Sprintf("Online now: %d device(s)", devices)
	if s.IPLimit > 0 {
		line += fmt.Sprintf(" (limit %d)", s.IPLimit)
	}
	var countries []string
	for _, o := range online {
		if o.Country != "" && !slices.Contains(countries, o.Country) {
			countries = append(countries, o.Country)
		}
	}
	if len(countries) > 0 {
		line += " from " + esc(strings.Join(countries, ", "))
	}
	b.WriteString(line + "\n")
	if s.LastOnlineAt > 0 && devices == 0 {
		fmt.Fprintf(&b, "Last online %s ago\n", humanDuration(now()-s.LastOnlineAt))
	}
	if flags := subFlags(s, devices, now()); len(flags) > 0 {
		fmt.Fprintf(&b, "Flags: %s (nothing is paused by them)\n", strings.ReplaceAll(strings.Join(flags, ", "), "_", " "))
	}
	return strings.TrimSpace(b.String())
}

func (p *Panel) botOnline(ctx context.Context) string {
	n := p.botNowData(ctx)
	byID := map[int64]*Sub{}
	for _, s := range n.subs {
		byID[s.ID] = s
	}
	type row struct {
		name    string
		devices int
		where   []string
	}
	var rows []row
	total := 0
	for id, list := range n.online {
		s := byID[id]
		if s == nil || len(list) == 0 {
			continue
		}
		r := row{name: s.Name, devices: distinctIPs(list)}
		for _, o := range list {
			if !slices.Contains(r.where, o.Server) {
				r.where = append(r.where, o.Server)
			}
		}
		total += r.devices
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return "Nobody is connected right now."
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].devices != rows[j].devices {
			return rows[i].devices > rows[j].devices
		}
		return rows[i].name < rows[j].name
	})
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Online now</b>: %d user(s) on %d device(s)\n", len(rows), total)
	for i, r := range rows {
		if i == 40 {
			fmt.Fprintf(&b, "… and %d more\n", len(rows)-40)
			break
		}
		fmt.Fprintf(&b, "🟢 %s - %d device(s) on %s\n", esc(r.name), r.devices, esc(strings.Join(r.where, ", ")))
	}
	return strings.TrimSpace(b.String())
}

// topUsers lists the users with the most traffic since a day.
func (p *Panel) topUsers(ctx context.Context, since string, n int) []string {
	subs, _ := p.subsOf(ctx, 0)
	names := map[int64]string{}
	for _, s := range subs {
		names[s.ID] = s.Name
	}
	use := p.userTraffic(ctx, since)
	ids := make([]int64, 0, len(use))
	for id, v := range use {
		if v > 0 && names[id] != "" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return use[ids[i]] > use[ids[j]] })
	var out []string
	for i, id := range ids {
		if i == n {
			break
		}
		out = append(out, fmt.Sprintf("%s %s", esc(names[id]), fmtBytes(use[id])))
	}
	return out
}

func (p *Panel) botTop(ctx context.Context) string {
	t := time.Now().In(p.loc())
	var b strings.Builder
	b.WriteString("<b>Most traffic today</b>\n")
	if top := p.topUsers(ctx, t.Format("2006-01-02"), 10); len(top) > 0 {
		for i, x := range top {
			fmt.Fprintf(&b, "%d. %s\n", i+1, x)
		}
	} else {
		b.WriteString("Nobody used any yet.\n")
	}
	b.WriteString("\n<b>This month</b>\n")
	if top := p.topUsers(ctx, t.Format("2006-01")+"-01", 10); len(top) > 0 {
		for i, x := range top {
			fmt.Fprintf(&b, "%d. %s\n", i+1, x)
		}
	} else {
		b.WriteString("Nobody used any yet.\n")
	}
	return strings.TrimSpace(b.String())
}

func (p *Panel) botEvents(ctx context.Context) string {
	rows, err := p.db.QueryContext(ctx, `SELECT ts, level, message FROM events ORDER BY id DESC LIMIT 12`)
	if err != nil {
		return "The events could not be read."
	}
	defer rows.Close()
	var b strings.Builder
	b.WriteString("<b>Latest events</b>\n")
	n := 0
	for rows.Next() {
		var ts int64
		var level, msg string
		if rows.Scan(&ts, &level, &msg) != nil {
			continue
		}
		n++
		fmt.Fprintf(&b, "%s %s %s\n", levelMark(level), time.Unix(ts, 0).In(p.loc()).Format("2 Jan 15:04"), esc(oneLineText(truncate(msg, 300))))
	}
	if n == 0 {
		return "Nothing happened yet."
	}
	return strings.TrimSpace(b.String())
}

// botRisks lists the open health risks, with buttons to decide about each when changes are allowed.
func (p *Panel) botRisks(ctx context.Context, c notifyConfig) (string, [][]tgButton) {
	list, err := p.risks(ctx, riskFilter{status: "open", limit: 100})
	if err != nil {
		return "The risks could not be read.", nil
	}
	if len(list) == 0 {
		return "🟢 No open health risks.", nil
	}
	var b strings.Builder
	var keys [][]tgButton
	fmt.Fprintf(&b, "<b>Open health risks</b> (%d)\n", len(list))
	for i, v := range list {
		if i == 10 {
			fmt.Fprintf(&b, "… and %d more in the panel\n", len(list)-10)
			break
		}
		fmt.Fprintf(&b, "\n%d. %s <b>%s</b> - %s\n%s\n", i+1, sevMark(v.Severity), esc(v.Server), esc(v.Title), esc(truncate(v.Detail, 300)))
		if c.Changes {
			id := strconv.FormatInt(v.ID, 10)
			keys = append(keys, []tgButton{{Text: fmt.Sprintf("%d: Acknowledge", i+1), Data: "a:" + id}, {Text: fmt.Sprintf("%d: Expected…", i+1), Data: "e:" + id}})
		}
	}
	if !c.Changes {
		b.WriteString("\nDecide about them in the panel (Monitor › Health), or allow changes from Telegram there (Settings › Notifications).")
	}
	return strings.TrimSpace(b.String()), keys
}

func sevMark(sev string) string {
	switch sev {
	case proto.SevCritical:
		return "🔴"
	case proto.SevHigh:
		return "🟠"
	case proto.SevWarning:
		return "🟡"
	}
	return "⚪"
}

// botExpiring: servers' paid periods and users' access ending within days, and data running out.
func (p *Panel) botExpiring(ctx context.Context, days int) string {
	ending, data := p.endingSoon(ctx, days)
	var b strings.Builder
	if len(ending) == 0 && len(data) == 0 {
		return fmt.Sprintf("🟢 Nothing ends within %d days and nobody is running out of data.", days)
	}
	if len(ending) > 0 {
		fmt.Fprintf(&b, "<b>Ending within %d days</b>\n%s\n", days, strings.Join(ending, "\n"))
	}
	if len(data) > 0 {
		fmt.Fprintf(&b, "\n<b>Running out of data</b>\n%s\n", strings.Join(data, "\n"))
	}
	return strings.TrimSpace(b.String())
}

// endingSoon lists paid periods and users' access that end within days, and who used 80% of their data.
func (p *Panel) endingSoon(ctx context.Context, days int) (ending, data []string) {
	t := time.Now().In(p.loc())
	servers, _ := p.serversOf(ctx, 0)
	for _, s := range servers {
		if s.ExpiresOn != "" {
			if d, err := time.ParseInLocation("2006-01-02", s.ExpiresOn, p.loc()); err == nil {
				left := int(d.Sub(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, p.loc())).Hours() / 24)
				switch {
				case left < 0:
					ending = append(ending, fmt.Sprintf("🔴 %s's paid period ended on %s", esc(s.Name), d.Format("2 January")))
				case left <= days:
					ending = append(ending, fmt.Sprintf("🟠 %s's paid period ends on %s (%s)", esc(s.Name), d.Format("2 January"), inDays(left)))
				}
			}
		}
		if s.BwLimit > 0 && s.BwUsed() >= s.BwLimit*8/10 {
			data = append(data, fmt.Sprintf("🟠 %s used %.0f%% of its %s plan", esc(s.Name), float64(s.BwUsed())*100/float64(s.BwLimit), fmtBytes(s.BwLimit)))
		}
	}
	subs, _ := p.subsOf(ctx, 0)
	for _, s := range subs {
		if s.Paused {
			continue
		}
		if s.ExpiresAt > 0 && s.ExpiresAt-now() <= int64(days)*86400 {
			if s.ExpiresAt <= now() {
				ending = append(ending, fmt.Sprintf("🔴 %s's access ended on %s - nothing is paused", esc(s.Name), p.dateText(s.ExpiresAt)))
			} else {
				ending = append(ending, fmt.Sprintf("🟠 %s's access ends on %s", esc(s.Name), p.dateText(s.ExpiresAt)))
			}
		}
		if s.Quota > 0 && s.Used() >= s.Quota*8/10 {
			data = append(data, fmt.Sprintf("🟠 %s used %.0f%% of %s", esc(s.Name), float64(s.Used())*100/float64(s.Quota), fmtBytes(s.Quota)))
		}
	}
	if len(ending) > 25 {
		ending = append(ending[:25], fmt.Sprintf("… and %d more", len(ending)-25))
	}
	if len(data) > 25 {
		data = append(data[:25], fmt.Sprintf("… and %d more", len(data)-25))
	}
	return
}

func inDays(n int) string {
	switch n {
	case 0:
		return "today"
	case 1:
		return "tomorrow"
	}
	return fmt.Sprintf("in %d days", n)
}

// ---------------------------------------------------------------- the daily report

// dailyReport is what the bot sends once a day: traffic today and this month per server, the top
// users, availability, servers offline, what ends soon, data running out and open health risks.
func (p *Panel) dailyReport(ctx context.Context) string {
	t := time.Now().In(p.loc())
	servers, _ := p.serversOf(ctx, 0)
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s - daily report</b>\n%s\n", esc(p.settings().SiteTitle), t.Format("Monday 2 January 2006, 15:04"))

	ids := make([]int64, 0, len(servers))
	for _, s := range servers {
		ids = append(ids, s.ID)
	}
	av := p.availabilityOf(ctx, ids, nil)
	up, known := 0, 0
	var down []string
	for _, s := range servers {
		if s.FirstSeenAt == 0 {
			continue
		}
		known++
		if s.Online {
			up++
		} else {
			down = append(down, fmt.Sprintf("%s (for %s)", esc(s.Name), humanDuration(now()-s.StatusChangedAt)))
		}
	}
	fmt.Fprintf(&b, "\n<b>Servers</b>: %d of %d online", up, known)
	if len(down) > 0 {
		b.WriteString("\n🔴 Offline: " + strings.Join(down, ", "))
	}
	b.WriteString("\n")

	td, md := p.serverTraffic(ctx, nil)
	var tToday, tMonth int64
	for _, s := range servers {
		tToday, tMonth = tToday+td[s.ID], tMonth+md[s.ID]
	}
	fmt.Fprintf(&b, "\n<b>Traffic</b>: today %s · this month %s\n", fmtBytes(tToday), fmtBytes(tMonth))
	sorted := slices.Clone(servers)
	sort.SliceStable(sorted, func(i, j int) bool { return md[sorted[i].ID] > md[sorted[j].ID] })
	for i, s := range sorted {
		if i == 20 {
			fmt.Fprintf(&b, "… and %d more\n", len(sorted)-20)
			break
		}
		if s.FirstSeenAt == 0 {
			continue
		}
		line := fmt.Sprintf("• %s - today %s · month %s", esc(s.Name), fmtBytes(td[s.ID]), fmtBytes(md[s.ID]))
		if a := av[s.ID]; a != nil && a.H24 != nil && *a.H24 < 99.95 {
			line += fmt.Sprintf(" · up %.1f%%", *a.H24)
		}
		b.WriteString(line + "\n")
	}
	if top := p.topUsers(ctx, t.Format("2006-01-02"), 5); len(top) > 0 {
		b.WriteString("Top users today: " + strings.Join(top, ", ") + "\n")
	}
	if top := p.topUsers(ctx, t.Format("2006-01")+"-01", 5); len(top) > 0 {
		b.WriteString("Top users this month: " + strings.Join(top, ", ") + "\n")
	}

	var low []string
	for _, s := range servers {
		if a := av[s.ID]; a != nil && a.H24 != nil && *a.H24 < 99.95 && s.FirstSeenAt != 0 {
			low = append(low, fmt.Sprintf("%s %.2f%%", esc(s.Name), *a.H24))
		}
	}
	if len(low) == 0 && known > 0 {
		b.WriteString("\n<b>Availability</b> (24 h): every server was up all day\n")
	} else if len(low) > 0 {
		b.WriteString("\n<b>Availability</b> (24 h): " + strings.Join(low, ", ") + "\n")
	}

	ending, data := p.endingSoon(ctx, 7)
	if len(ending) > 0 {
		b.WriteString("\n<b>Ending within 7 days</b>\n" + strings.Join(ending, "\n") + "\n")
	}
	if len(data) > 0 {
		b.WriteString("\n<b>Running out of data</b>\n" + strings.Join(data, "\n") + "\n")
	}

	risks, _ := p.risks(ctx, riskFilter{status: "open", limit: 1000})
	if len(risks) == 0 {
		b.WriteString("\n<b>Health</b>: 🟢 no open risks")
	} else {
		count := map[string]int{}
		for _, v := range risks {
			count[v.Severity]++
		}
		var parts []string
		for i := len(severities) - 1; i >= 0; i-- {
			if n := count[severities[i]]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, severities[i]))
			}
		}
		fmt.Fprintf(&b, "\n<b>Health</b>: %d open risk(s) - %s\n", len(risks), strings.Join(parts, ", "))
		for i, v := range risks {
			if i == 5 {
				break
			}
			fmt.Fprintf(&b, "%s %s - %s\n", sevMark(v.Severity), esc(v.Server), esc(v.Title))
		}
		b.WriteString("/risks to decide about them")
	}
	return strings.TrimSpace(b.String())
}

// reportLoop sends the daily report at the chosen hour (panel time), once a day. A panel that was
// down at that hour sends it when it is back, the same day.
func (p *Panel) reportLoop(ctx context.Context) {
	var retry time.Time
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if time.Now().Before(retry) {
			continue
		}
		var err error
		guard("daily report", func() { err = p.reportTick(ctx) })
		if err != nil {
			slog.Warn("daily report", "err", err)
			retry = time.Now().Add(5 * time.Minute)
		}
	}
}

// reportTick sends the daily report if it is due.
func (p *Panel) reportTick(ctx context.Context) error {
	c := p.notifyConfig()
	if !c.Report || c.TelegramToken == "" || c.TelegramChat == "" {
		return nil
	}
	t := time.Now().In(p.loc())
	today := t.Format("2006-01-02")
	if last, _ := p.getSetting("telegram_report_day"); last == today || t.Hour() < c.hour() {
		return nil
	}
	if err := p.botSend(ctx, c, p.dailyReport(ctx), nil); err != nil {
		return err
	}
	p.setSetting("telegram_report_day", today)
	p.setSetting("telegram_report_sent", strconv.FormatInt(now(), 10))
	return nil
}

// apiSendReport sends the daily report now (to try it).
func (p *Panel) apiSendReport(w http.ResponseWriter, r *http.Request, a *Account) error {
	c := p.notifyConfig()
	if c.TelegramToken == "" || c.TelegramChat == "" {
		return errStatus(http.StatusBadRequest, "set up Telegram first: the bot token and the chat")
	}
	res := notifyTestResult{Telegram: "ok"}
	if err := p.botSend(r.Context(), c, p.dailyReport(r.Context()), nil); err != nil {
		res.Telegram = err.Error()
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// ---------------------------------------------------------------- private chats (tglink.go)

// userCommands are the commands of a user's private chat with the bot.
func userCommands() []tgCommand {
	return []tgCommand{
		{"usage", "Your data: used, left, and until when"},
		{"devices", "Your devices connected now"},
		{"open", "Open your page"},
		{"unlink", "Unlink this Telegram account"},
		{"help", "What this bot does"},
	}
}

// openButton opens the Mini App, when the panel has an https address for it.
func (p *Panel) openButton(label string) [][]tgButton {
	if u := p.miniAppURL(); u != "" {
		return [][]tgButton{{{Text: label, WebApp: &tgWebApp{URL: u}}}}
	}
	return nil
}

// botPrivate answers a message in a private chat: a link code, a linked user's or the supervisor's
// commands, or - for anyone else - how to link their account.
func (p *Panel) botPrivate(ctx context.Context, c notifyConfig, m *tgMessage) {
	if !c.UserLink && !c.Panel {
		return // the bot answers its chat only
	}
	from := *m.From
	// a person gets a dozen replies a minute, everyone together a few a second
	if !p.limiter.allow("tgpm:"+strconv.FormatInt(from.ID, 10), 12, time.Minute) || !p.limiter.allow("tgpm", 120, time.Minute) {
		return
	}
	chat := strconv.FormatInt(m.Chat.ID, 10)
	send := func(text string, keys [][]tgButton) {
		if err := p.botSendTo(ctx, c, chat, text, keys); err != nil {
			slog.Warn("telegram reply", "err", err)
		}
	}
	words := strings.Fields(m.Text)
	cmd, arg := "", ""
	if len(words) > 0 && strings.HasPrefix(words[0], "/") {
		cmd, _, _ = strings.Cut(strings.ToLower(strings.TrimPrefix(words[0], "/")), "@")
		arg = strings.Join(words[1:], " ")
	}
	l := p.tgLinkOf(ctx, from.ID)
	if (cmd == "start" || cmd == "link") && arg != "" && (l == nil || cmd == "link") {
		send(p.botLinkCode(ctx, c, from, arg))
		return
	}
	switch {
	case l == nil:
		send(p.botStranger(c))
	case l.Supervisor:
		if !c.Panel {
			send("Opening the panel from Telegram is off. Turn it on in the panel (Settings › Notifications).", nil)
			return
		}
		_, _ = p.db.Exec1(`UPDATE tg_links SET last_used_at = ? WHERE id = ?`, now(), l.ID)
		switch cmd {
		case "open":
			send("The panel, in Telegram:", p.openButton("Open the panel"))
		case "unlink":
			tok := p.botPend(&botPending{kind: "unlink", linkID: l.ID, user: from.ID})
			send("Unlink this Telegram account from the panel? It can then no longer open the panel or use these commands here.",
				confirmButtons(tok, "Yes, unlink"))
		case "", "start":
			send(p.botHelp(c)+"\n/open - the panel in Telegram\n/unlink - unlink this Telegram account", p.openButton("Open the panel"))
		default:
			if !p.botAllow(ctx, c) {
				return
			}
			text, keys := p.botCommand(ctx, c, from, cmd, cleanName(arg, 64))
			send(text, keys)
		}
	default:
		if !c.UserLink {
			send("Using this bot is off for now - ask whoever runs this service.", nil)
			return
		}
		s, err := p.subByID(ctx, l.SubID)
		if err != nil || !s.CanSignIn {
			send("Your account cannot use this any more - ask whoever runs this service.", nil)
			return
		}
		_, _ = p.db.Exec1(`UPDATE tg_links SET last_used_at = ? WHERE id = ?`, now(), l.ID)
		send(p.botUserCommand(ctx, s, l, from, cmd))
	}
}

// botStranger tells someone whose account is not linked how to link it.
func (p *Panel) botStranger(c notifyConfig) (string, [][]tgButton) {
	site := esc(p.settings().SiteTitle)
	if !c.UserLink { // only the supervisor links theirs
		return "This is <b>" + site + "</b>'s bot. It answers the accounts linked to it.", p.openButton("Sign in")
	}
	if p.miniAppURL() != "" {
		return fmt.Sprintf("Hi! This is <b>%s</b>'s bot. To check your usage here, link your Telegram account: open the app below and sign in once "+
			"with the username and password you were given.\n\nOr open your page in a browser, choose <b>Link a Telegram account</b> under Telegram and "+
			"send me the code it shows: /link CODE", site), p.openButton("Sign in")
	}
	return fmt.Sprintf("Hi! This is <b>%s</b>'s bot. To check your usage here, link your Telegram account: open your page in a browser, "+
		"choose <b>Link a Telegram account</b> under Telegram and send me the code it shows: /link CODE", site), nil
}

// botLinkCode links the sender's Telegram account with a code from a user's page or the panel.
func (p *Panel) botLinkCode(ctx context.Context, c notifyConfig, from tgUser, code string) (string, [][]tgButton) {
	key := "tgcode:" + strconv.FormatInt(from.ID, 10)
	if p.limiter.exceeded(key, 5, time.Hour) {
		return "Too many wrong codes - try again in an hour.", nil
	}
	x, ok := p.takeTgCode(code)
	if !ok {
		p.limiter.allow(key, 5, time.Hour)
		return "That code is wrong or has expired (codes work for ten minutes, once). Make a new one and send it again.", nil
	}
	if (x.sub > 0 && !c.UserLink) || (x.account > 0 && !c.Panel) {
		return "Linking Telegram accounts is off now.", nil
	}
	if err := p.linkTelegram(ctx, from, x.sub, x.account); err != nil {
		return "Not linked: " + esc(err.Error()), nil
	}
	p.noteLinked(ctx, from, x.sub, x.account, "with a code")
	if x.account > 0 {
		p.botCommandsChanged()
		return "✅ Linked: this Telegram account opens the panel and uses its commands here. /help lists them.", p.openButton("Open the panel")
	}
	return "✅ Linked! Ask me any time:\n/usage - your data: used, left and until when\n/devices - your devices connected now\n/unlink - unlink this account",
		p.openButton("Open your page")
}

// botUserCommand answers a linked user.
func (p *Panel) botUserCommand(ctx context.Context, s *Sub, l *tgLink, from tgUser, cmd string) (string, [][]tgButton) {
	switch cmd {
	case "usage", "me", "traffic", "data":
		return p.botUsage(ctx, s), p.openButton("Open your page")
	case "devices", "online":
		return p.botDevices(ctx, s), nil
	case "open":
		if b := p.openButton("Open your page"); b != nil {
			return "Your page, in Telegram:", b
		}
		return "Your page opens in a browser: the address you sign in at.", nil
	case "unlink":
		if at := p.unlinkAllowedAt(ctx, s.ID); at > 0 {
			return "You can unlink a Telegram account once a month - the next time on " + p.dateText(at) + ".", nil
		}
		tok := p.botPend(&botPending{kind: "unlink", linkID: l.ID, user: from.ID})
		return "Unlink this Telegram account? You can unlink one once a month, so the next unlink is possible in 30 days.",
			confirmButtons(tok, "Yes, unlink")
	}
	return fmt.Sprintf("Hi %s! Ask me:\n/usage - your data: used, left and until when\n/devices - your devices connected now\n/open - your page\n/unlink - unlink this Telegram account",
		esc(s.Name)), p.openButton("Open your page")
}

// botUsage is a user's own usage: data used and left this cycle, its reset, today, their access and
// devices, and what is left of limits on single protocols.
func (p *Panel) botUsage(ctx context.Context, s *Sub) string {
	var b strings.Builder
	status := "active"
	if s.Paused {
		status = "⏸ paused"
	}
	fmt.Fprintf(&b, "<b>%s</b> - %s\n", esc(s.Name), status)
	if s.Quota > 0 {
		left := max(s.Quota-s.Used(), 0)
		fmt.Fprintf(&b, "Data: %s of %s used · <b>%s left</b>\n", fmtBytes(s.Used()), fmtBytes(s.Quota), fmtBytes(left))
	} else {
		fmt.Fprintf(&b, "Data: %s used this cycle (no limit)\n", fmtBytes(s.Used()))
	}
	switch s.CountMode {
	case "down":
		b.WriteString("Only downloads count.\n")
	case "up":
		b.WriteString("Only uploads count.\n")
	case "max":
		b.WriteString("The larger of downloads and uploads counts.\n")
	}
	if next := nextPeriod(s, p.localNow()); next > 0 {
		fmt.Fprintf(&b, "Starts over on %s\n", p.dateText(next))
	}
	var today int64
	_ = p.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(up + down), 0) FROM traffic_daily WHERE sub_id = ? AND day = ?`, s.ID, p.dayKey(time.Now())).Scan(&today)
	fmt.Fprintf(&b, "Today: %s\n", fmtBytes(today))
	if s.ExpiresAt > 0 {
		if s.ExpiresAt <= now() {
			fmt.Fprintf(&b, "Access ended on %s\n", p.dateText(s.ExpiresAt))
		} else {
			fmt.Fprintf(&b, "Access until %s (%s)\n", p.dateText(s.ExpiresAt), inDays(int((s.ExpiresAt-now())/86400)))
		}
	}
	devices := distinctIPs(p.onlineBySub(ctx, s.AccountID)[s.ID])
	line := fmt.Sprintf("Devices connected now: %d", devices)
	if s.IPLimit > 0 {
		line += fmt.Sprintf(" (of %d)", s.IPLimit)
	}
	b.WriteString(line + "\n")
	if limits := p.nodeLimitsOf(ctx, s); len(limits) > 0 {
		b.WriteString("\n<b>Limits on single protocols</b>\n")
		n := 0
		for _, l := range limits {
			if l.Removed {
				continue
			}
			if n == 10 {
				b.WriteString("… more on your page\n")
				break
			}
			n++
			name := l.Protocol
			if srv, err := p.serverByID(ctx, l.ServerID); err == nil {
				name = srv.ShownName() + " " + l.Protocol
			}
			if l.Stopped {
				fmt.Fprintf(&b, "• %s: used up - it works again when the cycle starts over\n", esc(name))
			} else {
				fmt.Fprintf(&b, "• %s: %s left of %s\n", esc(name), fmtBytes(l.Left), fmtBytes(l.Quota))
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// botDevices lists a user's devices connected now: where from and through which server - not their
// addresses (a chat is no place for them).
func (p *Panel) botDevices(ctx context.Context, s *Sub) string {
	online := p.onlineBySub(ctx, s.AccountID)[s.ID]
	type dev struct {
		where   string
		since   int64
		servers []string
	}
	byIP := map[string]*dev{}
	var order []string
	for _, o := range online {
		d := byIP[o.IP]
		if d == nil {
			where := strings.Trim(strings.TrimSpace(o.City)+", "+o.Country, ", ")
			if o.Org != "" {
				where = strings.Trim(where+" · "+o.Org, " ·")
			}
			d = &dev{where: nz(where, "somewhere unknown"), since: o.Since}
			byIP[o.IP] = d
			order = append(order, o.IP)
		}
		if o.Since > 0 && (d.since == 0 || o.Since < d.since) {
			d.since = o.Since
		}
		name := o.Server
		if srv, err := p.serverByID(ctx, o.ServerID); err == nil {
			name = srv.ShownName()
		}
		if !slices.Contains(d.servers, name) {
			d.servers = append(d.servers, name)
		}
	}
	if len(order) == 0 {
		return "No device of yours is connected right now."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>Connected now</b>: %d device(s)", len(order))
	if s.IPLimit > 0 {
		fmt.Fprintf(&b, " of %d allowed", s.IPLimit)
	}
	b.WriteString("\n")
	for i, ip := range order {
		if i == 20 {
			fmt.Fprintf(&b, "… and %d more\n", len(order)-20)
			break
		}
		d := byIP[ip]
		line := "🟢 " + esc(d.where) + " - via " + esc(strings.Join(d.servers, ", "))
		if d.since > 0 {
			line += " · since " + time.Unix(d.since, 0).In(p.loc()).Format("2 Jan 15:04")
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSpace(b.String())
}

// botPing lists the ping monitors with each server's last round trip and the last hour's loss (by
// name - the addresses measured stay in the panel).
func (p *Panel) botPing(ctx context.Context) string {
	mons, err := p.pingMonitorsOf(ctx, 0)
	if err != nil {
		return "The ping monitors could not be read."
	}
	if len(mons) == 0 {
		return "No ping monitors yet - add them in the panel (Monitor › Ping)."
	}
	names := map[int64]string{}
	if servers, err := p.serversOf(ctx, 0); err == nil {
		for _, s := range servers {
			names[s.ID] = s.Name
		}
	}
	var b strings.Builder
	b.WriteString("<b>Ping monitors</b> (last round · loss in the last hour)\n")
	for _, m := range mons {
		state := ""
		if !m.Enabled {
			state = " - paused"
		}
		fmt.Fprintf(&b, "\n<b>%s</b> (%s)%s\n", esc(m.Name), strings.ToUpper(m.Kind), state)
		latest := p.pingLatestOf(ctx, m.ID)
		if len(latest) == 0 && m.Enabled {
			b.WriteString("no rounds in the last hour\n")
		}
		for _, l := range latest {
			mark, val := "🟢", fmt.Sprintf("%.1f ms", l.Avg)
			switch {
			case l.Loss >= 0.999:
				mark, val = "🔴", "no answer"
			case l.Loss >= 0.2:
				mark = "🟠"
			}
			if l.Loss > 0 && l.Loss < 0.999 {
				val += fmt.Sprintf(" · %.0f%% lost", l.Loss*100)
			}
			fmt.Fprintf(&b, "%s %s - %s\n", mark, esc(nz(names[l.ServerID], "a removed server")), val)
		}
	}
	return strings.TrimSpace(b.String())
}
