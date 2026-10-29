package panel

import (
	"context"
	"fmt"
	"strings"
	"time"

	"meridian/internal/proto"
	"meridian/internal/update"
)

// Cores that upgrade themselves. A running server keeps its cores' versions until someone upgrades
// them - unless the supervisor turned on Settings › core_auto_upgrade: then, at night (03:00 to 05:00
// in the panel's time zone), the panel upgrades the Xray and Hysteria of one server at a time to the
// versions in Settings - each core restarts once and its users reconnect by themselves. An upgrade
// the panel started is not started again for the same server for a day, whatever came of it.

// coreAutoUpgrade starts the next automatic core upgrade, when one is due.
func (p *Panel) coreAutoUpgrade(ctx context.Context) {
	set := p.settings()
	if !set.CoreAutoUpgrade {
		return
	}
	if h := time.Now().In(p.loc()).Hour(); h < 3 || h >= 5 {
		return
	}
	kinds := []any{proto.ActionUpgradeXray, proto.ActionUpgradeHysteria}
	var busy int
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE status = 'pending' AND created_by = 0 AND kind IN (?, ?)`,
		kinds...).Scan(&busy); err != nil || busy > 0 {
		return // one server at a time
	}
	servers, err := p.serversOf(ctx, 0)
	if err != nil {
		return
	}
	for _, s := range servers {
		if !s.Online || s.Guest {
			continue
		}
		kind, from, to := p.coreBehind(s, set)
		if kind == "" {
			continue
		}
		var recent int
		_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actions WHERE server_id = ? AND kind = ? AND created_by = 0 AND created_at > ?`,
			s.ID, kind, now()-86400).Scan(&recent)
		if recent > 0 {
			continue // tried within a day
		}
		if _, err := p.db.Exec1(`INSERT INTO actions (server_id, kind, args, created_at, created_by) VALUES (?, ?, '{}', ?, 0)`,
			s.ID, kind, now()); err != nil {
			return
		}
		core := "Xray"
		if kind == proto.ActionUpgradeHysteria {
			core = "Hysteria"
		}
		p.event(s.AccountID, "warn", "core_upgrade", s.ID, 0, 0, fmt.Sprintf("Upgrading %s on %s from %s to %s by itself, at night (Settings › Cores): it restarts once and its users reconnect by themselves",
			core, s.Name, from, to), nil)
		p.touchServers(s.ID)
		return
	}
}

// coreBehind is the first core of a server that is older than the version in Settings: the action
// that upgrades it, and the versions from and to ("" when none is).
func (p *Panel) coreBehind(s *Server, set Settings) (string, string, string) {
	if s.XrayVersion != "" && set.XrayVersion != "" && update.Newer(set.XrayVersion, strings.TrimPrefix(s.XrayVersion, "v")) {
		return proto.ActionUpgradeXray, s.XrayVersion, set.XrayVersion
	}
	if ls := p.live.get(s.ID); ls != nil && set.HysteriaVersion != "" {
		for name, c := range ls.Live.Cores {
			v := strings.TrimPrefix(c.Version, "v")
			if strings.HasPrefix(name, "hysteria-") && v != "" && update.Newer(set.HysteriaVersion, v) {
				return proto.ActionUpgradeHysteria, v, set.HysteriaVersion
			}
		}
	}
	return "", "", ""
}
