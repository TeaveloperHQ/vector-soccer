package main

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"
)

// 대회 진행 — 교사 화면(호스트 WebSocket)의 명령을 받아 대진을 경기로 연다.

type hostCmd struct {
	T      string   `json:"t"`
	Name   string   `json:"name"`
	Type   CompType `json:"type"`
	Rules  Rules    `json:"rules"`
	Keys   []string `json:"keys"`
	ID     string   `json:"id"`
	Winner string   `json:"winner"` // "a" | "b" | "draw"
	Groups int      `json:"groups"` // 리그 조 수
}

func (h *Hub) hostErr(c *client, msg string) {
	h.sendTo(c, mustJSON(map[string]string{"t": "hostError", "message": msg}))
}

func (h *Hub) compChanged() {
	h.compDirty = true
	h.lobbyDirty = true
	if h.comp != nil {
		h.saveComp(h.comp)
	}
}

func (h *Hub) onHostMessage(in inMsg) {
	var cmd hostCmd
	if json.Unmarshal(in.data, &cmd) != nil {
		return
	}
	c := h.comp
	switch cmd.T {
	case "compCreate":
		if c != nil && !c.finished() {
			h.hostErr(in.c, "진행 중인 대회가 있습니다. 먼저 대회를 끝내세요.")
			return
		}
		entrants := []Entrant{}
		seen := map[string]bool{}
		for _, k := range cmd.Keys {
			for _, p := range h.players {
				if p.key() == k && !seen[k] {
					seen[k] = true
					entrants = append(entrants, Entrant{Key: k, SID: p.sid, Name: p.name})
				}
			}
		}
		nc, err := newCompetition(cmd.Name, cmd.Type, cmd.Rules, entrants, cmd.Groups, h.now())
		if err != nil {
			h.hostErr(in.c, err.Error())
			return
		}
		if c != nil { // 끝난 대회는 닫고 새로
			c.Closed = true
			h.saveComp(c)
		}
		h.comp = nc
		log.Printf("대회 시작: %s (%s, %d명, 경기 %d판)", nc.Name, nc.Type, len(nc.Entrants), len(nc.Fixtures))
		h.compChanged()
	case "compClose":
		if c == nil {
			return
		}
		for _, m := range h.matches {
			if m.fixture != nil && m.phase != phEnd {
				h.cancelMatch(m, "선생님이 대회를 끝냈습니다.")
			}
		}
		c.Closed = true
		h.saveComp(c)
		h.comp = nil
		h.compDirty, h.lobbyDirty = true, true
	case "compStartRound":
		if c == nil {
			return
		}
		r := c.currentRound()
		started, skipped := 0, 0
		for _, f := range c.Fixtures {
			if f.Round != r || f.Status != fxPending || !f.ready() {
				continue
			}
			if h.startFixture(f) == "" {
				started++
			} else {
				skipped++
			}
		}
		if skipped > 0 {
			h.hostErr(in.c, "접속하지 않았거나 다른 경기 중인 학생이 있어 "+strconv.Itoa(skipped)+"경기는 시작하지 못했습니다.")
		}
		h.compChanged()
	case "compStartFixture":
		if c == nil {
			return
		}
		f := c.fixture(cmd.ID)
		if f == nil || f.Status != fxPending || !f.ready() {
			return
		}
		if why := h.startFixture(f); why != "" {
			h.hostErr(in.c, why)
		}
		h.compChanged()
	case "compDecide": // 교사가 결과 지정(결석 부전승 등)
		if c == nil {
			return
		}
		f := c.fixture(cmd.ID)
		if f == nil || f.Status != fxPending || !f.ready() {
			return
		}
		switch cmd.Winner {
		case "a":
			c.decide(f, f.A, 0, 0, "teacher")
		case "b":
			c.decide(f, f.B, 0, 0, "teacher")
		case "draw":
			if c.Type == Tournament {
				h.hostErr(in.c, "토너먼트는 무승부가 없습니다.")
				return
			}
			c.decide(f, -1, 0, 0, "teacher")
		}
		h.compChanged()
	case "compReset": // 진행 중이면 취소, 끝났으면 결과 되돌리기
		if c == nil {
			return
		}
		f := c.fixture(cmd.ID)
		if f == nil {
			return
		}
		if f.Status == fxPlaying {
			for _, m := range h.matches {
				if m.fixture == f && m.phase != phEnd {
					h.cancelMatch(m, "선생님이 이 경기를 취소했습니다.")
				}
			}
			f.Status = fxPending
		} else if err := c.undo(f); err != nil {
			h.hostErr(in.c, err.Error())
			return
		}
		h.compChanged()
	}
}

// startFixture 는 대진의 두 학생을 찾아 경기를 연다. 못 열면 이유를 돌려준다.
func (h *Hub) startFixture(f *Fixture) string {
	c := h.comp
	a, b := h.onlinePlayer(c.Entrants[f.A].Key), h.onlinePlayer(c.Entrants[f.B].Key)
	for _, x := range []struct {
		p *Player
		e Entrant
	}{{a, c.Entrants[f.A]}, {b, c.Entrants[f.B]}} {
		if x.p == nil {
			return x.e.label() + " 학생이 접속해 있지 않습니다."
		}
		if x.p.match != nil && x.p.match.phase != phEnd {
			return x.e.label() + " 학생이 다른 경기 중입니다."
		}
	}
	for _, p := range []*Player{a, b} {
		h.detachEnded(p) // 끝난 경기 결과 화면에 있으면 거기서 데려온다
	}
	f.Status = fxPlaying
	h.startMatch(a, b, c.Rules, f, randInt(2)) // 대회 첫 선축은 무작위
	return ""
}

func (h *Hub) detachEnded(p *Player) {
	m := p.match
	if m == nil || m.phase != phEnd {
		return
	}
	p.match = nil
	if other := m.players[1-m.sideOf(p)]; other.match != m {
		h.dropMatch(m)
	}
}

// onlinePlayer 는 key 에 해당하는 접속 중인 학생(경기 중이 아닌 쪽 우선).
func (h *Hub) onlinePlayer(key string) *Player {
	var found *Player
	for _, p := range h.players {
		if p.conn == nil || p.key() != key {
			continue
		}
		if found == nil || (busyMatch(found) && !busyMatch(p)) {
			found = p
		}
	}
	return found
}

// cancelMatch 는 기록 없이 경기를 없애고 두 학생을 로비로 돌려보낸다.
func (h *Hub) cancelMatch(m *Match, msg string) {
	for _, p := range m.players {
		h.sendP(p, map[string]string{"t": "cancelled", "message": msg})
	}
	m.saved = true
	h.dropMatch(m)
}

// recordFixture 는 끝난 대회 경기의 결과를 대진에 반영한다.
func (h *Hub) recordFixture(m *Match) {
	f := m.fixture
	if f == nil || h.comp == nil || h.comp.fixture(f.ID) != f || f.Status != fxPlaying {
		return
	}
	winner := -1
	switch m.winner {
	case 0:
		winner = f.A
	case 1:
		winner = f.B
	}
	h.comp.decide(f, winner, m.score[0], m.score[1], m.reason)
	if h.comp.finished() {
		log.Printf("대회 종료: %s", h.comp.Name)
	}
	h.compChanged()
}

func (h *Hub) fixtureTitle(m *Match) string {
	if m.fixture == nil || h.comp == nil {
		return ""
	}
	return strings.TrimSpace(h.comp.Name + " · " + h.comp.groupName(m.fixture.Group) + " " + h.comp.roundName(m.fixture.Round))
}

// compInfoFor 는 학생 로비에 보여줄 "내 대회" 안내.
func (h *Hub) compInfoFor(p *Player) map[string]any {
	c := h.comp
	if c == nil {
		return nil
	}
	idx := c.entrantIndex(p.key())
	if idx < 0 {
		return nil
	}
	info := map[string]any{"name": c.Name, "type": c.Type, "group": c.groupName(c.Entrants[idx].Group)}
	for r := 0; r < c.Rounds; r++ {
		for _, f := range c.Fixtures {
			if f.Round != r || (f.A != idx && f.B != idx) {
				continue
			}
			if f.Status == fxDone {
				if c.Type == Tournament && f.Winner != idx {
					info["state"] = "out"
					info["round"] = c.roundName(r)
					return info
				}
				continue
			}
			info["round"] = c.roundName(r)
			if !f.ready() {
				info["state"] = "waiting" // 상대가 앞 경기에서 정해지는 중
				return info
			}
			opp := f.A
			if opp == idx {
				opp = f.B
			}
			info["state"] = f.Status // pending | playing
			info["opponent"] = c.Entrants[opp].label()
			return info
		}
	}
	info["state"] = "done"
	if c.Type == Tournament && c.champion() == idx {
		info["state"] = "champion"
	}
	if c.Type == League {
		for _, s := range c.standings() {
			if s.Entrant == idx {
				info["rank"] = s.Rank
				info["points"] = s.Points
			}
		}
	}
	return info
}

func (h *Hub) compMsg() []byte {
	c := h.comp
	if c == nil {
		return mustJSON(map[string]any{"t": "comp", "comp": nil})
	}
	online := make([]bool, len(c.Entrants))
	busy := make([]bool, len(c.Entrants))
	for i, e := range c.Entrants {
		if p := h.onlinePlayer(e.Key); p != nil {
			online[i] = true
			busy[i] = busyMatch(p) && p.match.fixture == nil
		}
	}
	names := make([]string, c.Rounds)
	for r := range names {
		names[r] = c.roundName(r)
	}
	groupNames := make([]string, c.groupCount())
	for g := range groupNames {
		groupNames[g] = c.groupName(g)
	}
	return mustJSON(map[string]any{
		"t": "comp", "comp": c, "standings": c.standings(), "current": c.currentRound(),
		"champion": c.champion(), "online": online, "busy": busy, "roundNames": names,
		"groupNames": groupNames,
	})
}

func busyMatch(p *Player) bool { return p.match != nil && p.match.phase != phEnd }
