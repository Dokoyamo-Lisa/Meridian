package panel

import (
	"context"
	"fmt"
	"sync"
	"time"

	"meridian/internal/proto"
)

// Out of data: a user who used up their data allowance is suspended by itself - the one limit that
// does that. From the report that takes them past their quota, no server serves them until the data
// starts over: their next cycle (resetCycles), a higher quota, a new period on a plan or their usage
// reset by hand. Then they are back by themselves, without anyone's click. Their link keeps working
// and says why, so their apps keep the profile and connect again the moment the data is back. The
// servers apply both live, like any change of users. A pause stays a person's decision: nothing
// here pauses or resumes anyone.

// outOfData says whether the user used up their data allowance this cycle.
func (s *Sub) outOfData() bool { return s.Quota > 0 && s.Used() >= s.Quota }

// What they have open when it happens follows the supervisor's choice (Settings.QuotaMode): strict
// cuts it at once on every protocol; loose lets it go on until QuotaGraceMin minutes have passed or
// QuotaGraceGB GB more were used, then cuts it. New connections are refused at once in both. The
// servers' agents do the cutting (State.Grace lists who may keep what is open, and until when).

// graceBytes is how much more what is open may use in loose mode.
func (set Settings) graceBytes() int64 { return int64(set.QuotaGraceGB) << 30 }

// graceUntil says until when what an out-of-data user has open may go on: 0 = not at all (strict
// mode, the extra data used up too, or the time is over).
func graceUntil(s *Sub, set Settings, t int64) int64 {
	if set.QuotaMode != "loose" || s.Paused || !s.outOfData() || s.OutAt == 0 {
		return 0
	}
	until := s.OutAt + int64(set.QuotaGraceMin)*60
	if t >= until || s.Used() >= s.Quota+set.graceBytes() {
		return 0
	}
	return until
}

// markOut keeps out_at in step with the data: set when a user's data runs out, cleared once they
// have data again.
func (p *Panel) markOut(ctx context.Context, s *Sub) {
	switch {
	case s.outOfData() && s.OutAt == 0:
		s.OutAt = now()
		_, err := p.db.ExecContext(ctx, `UPDATE subs SET out_at = ? WHERE id = ? AND out_at = 0`, s.OutAt, s.ID)
		logErr("mark out of data", err)
	case !s.outOfData() && s.OutAt != 0:
		s.OutAt = 0
		_, err := p.db.ExecContext(ctx, `UPDATE subs SET out_at = 0 WHERE id = ?`, s.ID)
		logErr("mark out of data", err)
	}
}

// markOutAll does markOut for every user of an account (0 = all accounts).
func (p *Panel) markOutAll(ctx context.Context, acct int64) {
	subs, err := p.subsOf(ctx, acct)
	if err != nil {
		return
	}
	for _, s := range subs {
		p.markOut(ctx, s)
	}
}

// quotaCrossed lists the users of an account whose data a report's traffic used up - every server
// must stop serving them - and says whether it took one in loose mode past the extra data what they
// have open may use: then that is cut everywhere (checked after the traffic is counted).
func (p *Panel) quotaCrossed(ctx context.Context, acct int64, counted []proto.UserTraffic) (out []*Sub, graceOver bool) {
	set := p.settings()
	added := map[int64][2]int64{}
	for _, u := range counted {
		if u.Up < 0 || u.Down < 0 {
			continue
		}
		a := added[u.Sub]
		added[u.Sub] = [2]int64{a[0] + u.Up, a[1] + u.Down}
	}
	for id, a := range added {
		s, err := p.subByID(ctx, id)
		if err != nil || s.AccountID != acct || s.Paused || !s.outOfData() {
			continue
		}
		before := countedUsage(s.CountMode, s.CycleUp-a[0], s.CycleDown-a[1])
		if before < s.Quota {
			out = append(out, s)
		} else if limit := s.Quota + set.graceBytes(); set.QuotaMode == "loose" && before < limit && s.Used() >= limit {
			graceOver = true
		}
	}
	return out, graceOver
}

var quotaEventMu sync.Mutex // reports and the limits job may find the same user at once

// quotaReached records, once a cycle, that a user used up their data and is suspended until it
// starts over - for the timeline and the notifications.
func (p *Panel) quotaReached(ctx context.Context, s *Sub) {
	if s.Paused || !s.outOfData() {
		return
	}
	quotaEventMu.Lock()
	defer quotaEventMu.Unlock()
	if p.eventSince(ctx, "quota_reached", s.ID, s.CycleStart) {
		return
	}
	msg := fmt.Sprintf("%s used all of their %s - suspended: no server serves them until you give them more data (raise their quota or reset their usage)",
		s.Name, fmtBytes(s.Quota))
	if back := nextPeriod(s, p.localNow()); back > 0 {
		msg = fmt.Sprintf("%s used all of their %s this cycle - suspended: no server serves them until their data starts over on %s",
			s.Name, fmtBytes(s.Quota), p.dateText(back))
	}
	set := p.settings()
	if until := graceUntil(s, set, now()); until > 0 {
		msg += fmt.Sprintf(". What they have open may go on until %s or %d GB more (loose mode)", time.Unix(until, 0).In(p.loc()).Format("15:04"), set.QuotaGraceGB)
	} else {
		msg += ". Everything they had open was cut"
	}
	p.event(s.AccountID, "warn", "quota_reached", 0, s.ID, 0, msg, nil)
}

// eventSince says whether a user had an event of a kind since a time.
func (p *Panel) eventSince(ctx context.Context, kind string, subID, since int64) bool {
	var n int
	_ = p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE kind = ? AND sub_id = ? AND ts >= ?`, kind, subID, since).Scan(&n)
	return n > 0
}

// backOn says, in a few words, when a user whose data is used up is served again: "on 1 Nov"
// when their data starts over by itself, else "when they get more data".
func (p *Panel) backOn(s *Sub) string {
	if back := nextPeriod(s, p.localNow()); back > 0 {
		return "on " + time.Unix(back, 0).In(p.loc()).Format("2 Jan")
	}
	return "when they get more data"
}

// outText tells the user themself, on their link's page and in Telegram, that their data is used up
// and when they can connect again.
func (p *Panel) outText(s *Sub) string {
	if back := nextPeriod(s, p.localNow()); back > 0 {
		return fmt.Sprintf("Your data is used up. You can connect again on %s, when it starts over.", p.dateText(back))
	}
	return "Your data is used up. Ask your administrator for more."
}
