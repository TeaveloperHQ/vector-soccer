package main

import (
	"crypto/rand"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

// 연결·로비·경기 진행 레이어. 모든 상태 변경은 run() 단일 고루틴 안에서만 일어나므로
// 락이 필요 없다(액터 모델 — classroom-quiz 와 같은 구조). 물리·규칙은 match.go 에 있다.

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10

	tickPeriod    = time.Second / 60
	stateEvery    = 2 // 60Hz 틱 중 2틱마다 경기 상태 송신 = 30Hz
	hostEvery     = 6 // 교사 화면 갱신 = 10Hz
	inviteTTL     = 30 * time.Second
	endLinger     = 60 * time.Second // 경기 종료 화면(다시 하기) 유지 시간
	offlineForget = 2 * time.Minute  // 끊긴 채 경기 밖에 있는 학생을 명단에서 지우기까지
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true }, // 로컬 교실 전용
}

type client struct {
	hub   *Hub
	conn  *websocket.Conn
	send  chan []byte
	host  bool
	token string
	sid   string
	name  string
}

type inMsg struct {
	c    *client
	data []byte
}

// Player 는 학생 한 명. 연결(client)과 분리되어 있어 재접속하면 conn 만 갈아낀다.
type Player struct {
	id       string // 공개 식별자(다른 학생에게 보임)
	token    string // 재접속 비밀(본인만 앎)
	sid      string // 학번
	name     string
	conn     *client
	offSince time.Time

	room  *Room
	match *Match
}

func (p *Player) key() string { return playerKey(p.sid, p.name) }

func (p *Player) label() string {
	if p.sid != "" {
		return p.sid + " " + p.name
	}
	return p.name
}

// Room 은 상대를 기다리는 열린 방.
type Room struct {
	code  string
	owner *Player
	rules Rules
}

type invite struct {
	from  *Player
	rules Rules
	at    time.Time
}

type Hub struct {
	register   chan *client
	unregister chan *client
	inbound    chan inMsg

	hosts   map[*client]bool
	players map[string]*Player // 토큰 → 학생
	byID    map[string]*Player
	rooms   map[string]*Room
	invites map[*Player]map[*Player]invite // 받는 사람 → 보낸 사람 → 초대
	matches map[string]*Match
	ended   map[*Match]time.Time

	comp      *Competition // 교사가 연 대회(없으면 nil)
	compDirty bool

	lobbyDirty bool
	tick       int
	now        func() time.Time
	save       func(*Match)       // 결과 저장(테스트에서 교체)
	saveComp   func(*Competition) // 대회 저장(테스트에서 교체)
}

func newHub() *Hub {
	h := &Hub{
		register:   make(chan *client),
		unregister: make(chan *client),
		inbound:    make(chan inMsg, 256),
		hosts:      map[*client]bool{},
		players:    map[string]*Player{},
		byID:       map[string]*Player{},
		rooms:      map[string]*Room{},
		invites:    map[*Player]map[*Player]invite{},
		matches:    map[string]*Match{},
		ended:      map[*Match]time.Time{},
		now:        time.Now,
		saveComp:   saveCompetition,
	}
	h.save = h.saveMatchResult
	return h
}

func (h *Hub) run() {
	t := time.NewTicker(tickPeriod)
	defer t.Stop()
	for {
		select {
		case c := <-h.register:
			h.onRegister(c)
		case c := <-h.unregister:
			h.onUnregister(c)
		case m := <-h.inbound:
			h.onMessage(m)
		case <-t.C:
			h.onTick()
		}
	}
}

// ── 연결 ────────────────────────────────────────────────────

func (h *Hub) onRegister(c *client) {
	if c.host {
		h.hosts[c] = true
		h.sendTo(c, h.hostMsg())
		h.sendTo(c, h.compMsg())
		return
	}
	p, ok := h.players[c.token]
	if ok {
		if p.conn != nil && p.conn != c { // 같은 학생이 새 탭으로 들어옴 → 이전 연결 정리
			close(p.conn.send)
		}
		p.conn = c
		p.offSince = time.Time{}
		p.sid, p.name = c.sid, c.name
		log.Printf("학생 재접속: %s", p.label())
	} else if old := h.waitingPlayer(playerKey(c.sid, c.name)); old != nil {
		// 브라우저를 닫았다 새로 들어옴 — 같은 학번·이름이 끊긴 채 기다리는 경기가 있으면 그 학생으로 복귀
		delete(h.players, old.token)
		old.token = c.token
		h.players[c.token] = old
		p = old
		p.conn = c
		p.offSince = time.Time{}
		log.Printf("학생 복귀(새 연결): %s", p.label())
	} else {
		p = &Player{id: newID(), token: c.token, sid: c.sid, name: c.name, conn: c}
		h.players[c.token] = p
		h.byID[p.id] = p
		log.Printf("학생 입장: %s — 총 %d명", p.label(), len(h.players))
	}
	h.sendTo(c, mustJSON(map[string]any{"t": "hello", "id": p.id, "name": p.name, "sid": p.sid}))
	if m := p.match; m != nil {
		h.sendTo(c, mustJSON(h.matchStartMsg(m, m.sideOf(p))))
		if m.phase == phPause && h.bothOnline(m) {
			m.unpause()
		}
		h.sendTo(c, mustJSON(h.matchState(m, m.sideOf(p))))
	}
	h.lobbyDirty = true
	h.compDirty = true
}

// waitingPlayer 는 key 학생 중 연결이 끊겨 경기가 멈춰 있는 학생.
func (h *Hub) waitingPlayer(key string) *Player {
	for _, p := range h.players {
		if p.conn == nil && p.key() == key && p.match != nil && p.match.phase == phPause {
			return p
		}
	}
	return nil
}

func (h *Hub) onUnregister(c *client) {
	if c.host {
		delete(h.hosts, c)
		close(c.send)
		return
	}
	p, ok := h.players[c.token]
	if !ok || p.conn != c { // 이미 새 연결로 교체됨
		if !ok {
			close(c.send)
		}
		return
	}
	close(c.send)
	p.conn = nil
	p.offSince = h.now()
	log.Printf("학생 연결 끊김: %s", p.label())
	h.closeRoom(p)
	delete(h.invites, p)
	for _, inv := range h.invites {
		delete(inv, p)
	}
	if m := p.match; m != nil && m.phase != phEnd {
		m.pauseForReconnect()
	}
	h.lobbyDirty = true
	h.compDirty = true
}

func (h *Hub) sendTo(c *client, b []byte) {
	if c == nil {
		return
	}
	select {
	case c.send <- b:
	default: // 느린 연결은 이번 프레임을 건너뛴다(다음 상태가 전체를 다시 보냄)
	}
}

func (h *Hub) sendP(p *Player, v any) {
	if p != nil && p.conn != nil {
		h.sendTo(p.conn, mustJSON(v))
	}
}

func (h *Hub) sendErr(p *Player, msg string) {
	h.sendP(p, map[string]string{"t": "error", "message": msg})
}

// ── 메시지 ──────────────────────────────────────────────────

type clientMsg struct {
	T      string  `json:"t"`
	Rules  Rules   `json:"rules"`
	Code   string  `json:"code"`
	To     string  `json:"to"`
	From   string  `json:"from"`
	Accept bool    `json:"accept"`
	DX     float64 `json:"dx"`
	DY     float64 `json:"dy"`
}

func (h *Hub) onMessage(in inMsg) {
	if in.c.host {
		h.onHostMessage(in)
		return
	}
	p, ok := h.players[in.c.token]
	if !ok || p.conn != in.c {
		return
	}
	var msg clientMsg
	if json.Unmarshal(in.data, &msg) != nil {
		return
	}
	switch msg.T {
	case "blow":
		if m := p.match; m != nil {
			m.applyBlow(m.sideOf(p), vec{msg.DX, msg.DY})
		}
	case "create":
		h.createRoom(p, msg.Rules)
	case "cancel":
		h.closeRoom(p)
		h.lobbyDirty = true
	case "join":
		h.joinRoom(p, strings.TrimSpace(msg.Code))
	case "invite":
		h.sendInvite(p, msg.To, msg.Rules)
	case "inviteReply":
		h.replyInvite(p, msg.From, msg.Accept)
	case "rematch":
		h.requestRematch(p)
	case "leave":
		h.leaveMatch(p)
	}
}

func (h *Hub) createRoom(p *Player, rules Rules) {
	if p.match != nil {
		return
	}
	if !rules.valid() {
		h.sendErr(p, "경기 규칙이 올바르지 않습니다.")
		return
	}
	h.closeRoom(p)
	code := ""
	for i := 0; i < 50; i++ {
		c := randDigits(4)
		if _, used := h.rooms[c]; !used {
			code = c
			break
		}
	}
	if code == "" {
		h.sendErr(p, "방을 더 만들 수 없습니다.")
		return
	}
	r := &Room{code: code, owner: p, rules: rules}
	h.rooms[code] = r
	p.room = r
	h.lobbyDirty = true
}

func (h *Hub) closeRoom(p *Player) {
	if p.room != nil {
		delete(h.rooms, p.room.code)
		p.room = nil
	}
}

func (h *Hub) joinRoom(p *Player, code string) {
	r, ok := h.rooms[code]
	switch {
	case p.match != nil:
		return
	case !ok:
		h.sendErr(p, "그런 방이 없습니다. 방 번호를 확인하세요.")
		return
	case r.owner == p:
		h.sendErr(p, "내가 만든 방입니다. 친구가 들어오기를 기다리세요.")
		return
	}
	h.startMatch(r.owner, p, r.rules, nil, 1) // 들어온 사람이 선축
}

func (h *Hub) sendInvite(p *Player, toID string, rules Rules) {
	to, ok := h.byID[toID]
	switch {
	case p.match != nil:
		return
	case !ok || to.conn == nil:
		h.sendErr(p, "그 친구는 지금 접속해 있지 않습니다.")
		return
	case to == p:
		return
	case to.match != nil:
		h.sendErr(p, to.name+" 학생은 경기 중입니다.")
		return
	case !rules.valid():
		h.sendErr(p, "경기 규칙이 올바르지 않습니다.")
		return
	}
	if h.invites[to] == nil {
		h.invites[to] = map[*Player]invite{}
	}
	h.invites[to][p] = invite{from: p, rules: rules, at: h.now()}
	h.lobbyDirty = true
}

func (h *Hub) replyInvite(p *Player, fromID string, accept bool) {
	from, ok := h.byID[fromID]
	if !ok {
		return
	}
	inv, ok := h.invites[p][from]
	if !ok {
		return
	}
	delete(h.invites[p], from)
	h.lobbyDirty = true
	if !accept {
		h.sendP(from, map[string]string{"t": "notice", "message": p.name + " 학생이 초대를 거절했습니다."})
		return
	}
	if from.conn == nil || from.match != nil {
		h.sendErr(p, from.name+" 학생이 지금은 경기할 수 없습니다.")
		return
	}
	if p.match != nil {
		return
	}
	h.startMatch(from, p, inv.rules, nil, 1) // 초대 받은 사람이 선축
}

func (h *Hub) startMatch(a, b *Player, rules Rules, fx *Fixture, first int) *Match {
	h.closeRoom(a)
	h.closeRoom(b)
	delete(h.invites, a)
	delete(h.invites, b)
	for _, inv := range h.invites {
		delete(inv, a)
		delete(inv, b)
	}
	m := newMatch(newID(), a, b, rules, h.now(), first)
	m.fixture = fx
	h.matches[m.id] = m
	a.match, b.match = m, m
	log.Printf("경기 시작: %s vs %s (시간 %ds, 목표 %d골)", a.label(), b.label(), rules.TimeSec, rules.Goals)
	h.sendP(a, h.matchStartMsg(m, 0))
	h.sendP(b, h.matchStartMsg(m, 1))
	h.lobbyDirty = true
	return m
}

func (h *Hub) requestRematch(p *Player) {
	m := p.match
	if m == nil || m.phase != phEnd || m.fixture != nil { // 대회 경기는 다시 하기 없음
		return
	}
	side := m.sideOf(p)
	m.rematch[side] = true
	h.broadcastMatch(m) // 종료 카드의 "다시 하기" 상태 갱신
	other := m.players[1-side]
	if !m.rematch[1-side] {
		h.sendP(other, map[string]string{"t": "notice", "message": p.name + " 학생이 한 판 더 하자고 합니다."})
		return
	}
	if other.match != m || other.conn == nil {
		h.sendErr(p, "상대가 나갔습니다.")
		return
	}
	h.dropMatch(m)
	// 진영을 바꿔서. 선축은 직전 경기 진 사람, 비겼으면 지난번에 선축이 아니었던 사람.
	// 진영이 바뀌므로 이전 side s 는 새 경기에서 1-s.
	kickOld := 1 - m.firstKick
	if m.winner >= 0 {
		kickOld = 1 - m.winner
	}
	h.startMatch(m.players[1], m.players[0], m.rules, nil, 1-kickOld)
}

func (h *Hub) leaveMatch(p *Player) {
	m := p.match
	if m == nil {
		return
	}
	side := m.sideOf(p)
	if m.phase == phPause { // 상대 연결이 끊겨 기다리다 그만둠 — 누구의 기권도 아니다
		log.Printf("경기 취소(연결 끊긴 상대를 기다리다): %s vs %s", m.players[0].label(), m.players[1].label())
		h.cancelMatch(m, p.name+" 학생이 기다리지 않고 경기를 끝냈습니다. 기록은 남지 않아요.")
		h.lobbyDirty = true
		return
	}
	if m.phase != phEnd {
		m.forfeit(side)
		h.endMatch(m)
	}
	p.match = nil
	other := m.players[1-side]
	if other.match == m {
		h.sendP(other, map[string]string{"t": "notice", "message": p.name + " 학생이 나갔습니다."})
	} else {
		h.dropMatch(m)
	}
	h.lobbyDirty = true
}

func (h *Hub) endMatch(m *Match) {
	if !m.saved {
		m.saved = true
		h.ended[m] = h.now()
		if m.played > 0 || m.score != [2]int{} {
			h.save(m)
		}
		h.recordFixture(m)
		log.Printf("경기 종료: %s %d : %d %s (%s)",
			m.players[0].label(), m.score[0], m.score[1], m.players[1].label(), m.reason)
	}
	h.broadcastMatch(m) // 종료 상태를 즉시 전달
	h.lobbyDirty = true
}

func (h *Hub) dropMatch(m *Match) {
	if m.phase != phEnd && m.fixture != nil && m.fixture.Status == fxPlaying {
		m.fixture.Status = fxPending // 끝나지 못한 대회 경기는 다시 할 수 있게
		h.compChanged()
	}
	delete(h.matches, m.id)
	delete(h.ended, m)
	for _, p := range m.players {
		if p.match == m {
			p.match = nil
		}
	}
	h.lobbyDirty = true
}

func (h *Hub) bothOnline(m *Match) bool {
	return m.players[0].conn != nil && m.players[1].conn != nil
}

// ── 틱 ──────────────────────────────────────────────────────

func (h *Hub) onTick() {
	h.tick++
	now := h.now()
	for _, m := range h.matches {
		if m.phase == phEnd {
			if now.Sub(h.ended[m]) > endLinger {
				h.dropMatch(m)
			}
			continue
		}
		m.step(tickPeriod)
		if m.phase == phPause && m.phaseLeft > BothGoneWait && m.players[0].conn == nil && m.players[1].conn == nil {
			// 둘 다 오래 돌아오지 않음 — 누구의 기권도 아니므로 기록하지 않고 정리한다.
			log.Printf("경기 중단(둘 다 연결 끊김): %s vs %s", m.players[0].label(), m.players[1].label())
			h.dropMatch(m)
			continue
		}
		if m.phase == phEnd {
			h.endMatch(m)
			continue
		}
		if h.tick%stateEvery == 0 {
			h.broadcastMatch(m)
		}
	}

	if h.tick%30 == 0 { // 0.5초마다 청소
		for to, invs := range h.invites {
			for from, inv := range invs {
				if now.Sub(inv.at) > inviteTTL {
					delete(invs, from)
					h.lobbyDirty = true
				}
			}
			if len(invs) == 0 {
				delete(h.invites, to)
			}
		}
		for tok, p := range h.players {
			if p.conn == nil && p.match == nil && now.Sub(p.offSince) > offlineForget {
				delete(h.players, tok)
				delete(h.byID, p.id)
				h.lobbyDirty = true
			}
		}
	}

	if h.compDirty && h.tick%6 == 0 {
		h.compDirty = false
		if len(h.hosts) > 0 {
			b := h.compMsg()
			for c := range h.hosts {
				h.sendTo(c, b)
			}
		}
	}
	if h.lobbyDirty && h.tick%6 == 0 {
		h.lobbyDirty = false
		h.broadcastLobby()
	}
	if len(h.hosts) > 0 && h.tick%hostEvery == 0 {
		b := h.hostMsg()
		for c := range h.hosts {
			h.sendTo(c, b)
		}
	}
}

// ── 송신 메시지 ─────────────────────────────────────────────

func r2(x float64) float64 { return math.Round(x*100) / 100 }

func (h *Hub) matchStartMsg(m *Match, side int) map[string]any {
	return map[string]any{
		"t": "match", "id": m.id, "side": side, "rules": m.rules,
		"names": [2]string{m.players[0].label(), m.players[1].label()},
		"comp":  h.fixtureTitle(m),
		"field": map[string]float64{
			"w": FieldW, "h": FieldH, "goalTop": GoalTop, "goalBottom": GoalBottom,
			"goalDepth": GoalDepth, "ballR": BallR, "maxF": BlowMaxF, "friction": Friction,
			"cooldown": Cooldown.Seconds(), "missPenalty": MissPenalty.Seconds(),
		},
	}
}

// matchState 는 side 학생에게 보낼 경기 상태. 쿨타임은 본인 것만 보낸다(상대 쿨타임은 비공개).
func (h *Hub) matchState(m *Match, side int) map[string]any {
	forces := make([][3]float64, 0, len(m.blows))
	for _, b := range m.blows {
		forces = append(forces, [3]float64{float64(b.side), r2(b.force.X), r2(b.force.Y)})
	}
	st := map[string]any{
		"t": "s", "ph": m.phase,
		"b":  [4]float64{r2(m.ball.X), r2(m.ball.Y), r2(m.vel.X), r2(m.vel.Y)},
		"sc": m.score, "f": forces,
		"cd": int(m.cooldownLeft(side).Milliseconds()),
		"ct": int(m.cdTotal[side].Milliseconds()),
		"pl": int(m.phaseLeft.Milliseconds()),
		"on": [2]bool{m.players[0].conn != nil, m.players[1].conn != nil},
		"ko": m.kickOffSide(),
	}
	if m.rules.TimeSec > 0 {
		st["tl"] = int(m.playLeft.Milliseconds())
	}
	if m.golden {
		st["gg"] = true
	}
	if m.rules.TimeSec > 0 {
		st["it"] = int(m.injuryShown().Milliseconds()) // 추가시간
		st["inj"] = m.inInjury
	}
	switch m.phase {
	case phGoal:
		st["scorer"] = m.scorer
	case phEnd:
		st["winner"] = m.winner
		st["reason"] = m.reason
		st["rematch"] = m.rematch
		st["blows"] = m.nBlows
		st["misses"] = m.nMisses
	}
	return st
}

func (h *Hub) broadcastMatch(m *Match) {
	for side, p := range m.players {
		if p.match == m && p.conn != nil {
			h.sendTo(p.conn, mustJSON(h.matchState(m, side)))
		}
	}
}

type lobbyPlayer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // "idle" | "room" | "match"
}

func (h *Hub) broadcastLobby() {
	list := []lobbyPlayer{}
	for _, p := range h.players {
		if p.conn == nil {
			continue
		}
		st := "idle"
		if p.match != nil {
			st = "match"
		} else if p.room != nil {
			st = "room"
		}
		list = append(list, lobbyPlayer{p.id, p.label(), st})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	type roomView struct {
		Code  string `json:"code"`
		Owner string `json:"owner"`
		Rules Rules  `json:"rules"`
	}
	rooms := []roomView{}
	for _, r := range h.rooms {
		rooms = append(rooms, roomView{r.code, r.owner.label(), r.rules})
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i].Code < rooms[j].Code })

	for _, p := range h.players {
		if p.conn == nil {
			continue
		}
		type invView struct {
			From  string `json:"from"`
			Name  string `json:"name"`
			Rules Rules  `json:"rules"`
		}
		invs := []invView{}
		for from, inv := range h.invites[p] {
			invs = append(invs, invView{from.id, from.label(), inv.rules})
		}
		sent := []string{}
		for to, m := range h.invites {
			if _, ok := m[p]; ok {
				sent = append(sent, to.id)
			}
		}
		myRoom := ""
		if p.room != nil {
			myRoom = p.room.code
		}
		h.sendP(p, map[string]any{
			"t": "lobby", "players": list, "rooms": rooms,
			"invites": invs, "sent": sent, "myRoom": myRoom,
			"comp": h.compInfoFor(p),
		})
	}
}

func (h *Hub) hostMsg() []byte {
	type hp struct {
		Key    string `json:"key"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Online bool   `json:"online"`
	}
	online := map[string]bool{}
	for _, p := range h.players {
		if p.conn != nil {
			online[p.label()] = true
		}
	}
	players := []hp{}
	for _, p := range h.players {
		if p.conn == nil && online[p.label()] {
			continue // 새 탭·새 기기로 다시 들어온 같은 학생 — 끊긴 이전 항목은 숨긴다
		}
		st := "idle"
		if p.match != nil {
			st = "match"
		} else if p.room != nil {
			st = "room"
		}
		players = append(players, hp{p.key(), p.label(), st, p.conn != nil})
	}
	sort.Slice(players, func(i, j int) bool { return players[i].Name < players[j].Name })

	type hm struct {
		ID    string     `json:"id"`
		Names [2]string  `json:"names"`
		Score [2]int     `json:"score"`
		Phase phase      `json:"ph"`
		TL    int        `json:"tl"`
		Ball  [2]float64 `json:"b"`
		Rules Rules      `json:"rules"`
		Win   int        `json:"winner"`
		Inj   bool       `json:"inj"`
		Start int64      `json:"start"`
	}
	matches := []hm{}
	for _, m := range h.matches {
		tl := -1
		if m.rules.TimeSec > 0 {
			tl = int(m.playLeft.Milliseconds())
		}
		matches = append(matches, hm{
			m.id, [2]string{m.players[0].label(), m.players[1].label()}, m.score, m.phase, tl,
			[2]float64{r2(m.ball.X), r2(m.ball.Y)}, m.rules, m.winner, m.inInjury, m.startedAt.UnixMilli(),
		})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Start < matches[j].Start })
	return mustJSON(map[string]any{"t": "host", "players": players, "matches": matches, "rooms": len(h.rooms)})
}

// ── WebSocket 펌프 ──────────────────────────────────────────

// serveWS — ?role=host  또는  ?name=..&sid=..&token=..
func (h *Hub) serveWS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c := &client{hub: h, send: make(chan []byte, 64)}
	if q.Get("role") == "host" {
		if !isLoopback(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		c.host = true
	} else {
		c.name = cleanText(q.Get("name"), 12)
		c.sid = cleanText(q.Get("sid"), 8)
		c.token = cleanText(q.Get("token"), 40)
		if c.name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if c.token == "" {
			c.token = newID()
		}
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WS 업그레이드 실패: %v", err)
		return
	}
	c.conn = conn
	h.register <- c
	go c.writePump()
	go c.readPump()
}

func (c *client) readPump() {
	defer func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()
	c.conn.SetReadLimit(2048)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		c.hub.inbound <- inMsg{c: c, data: msg}
	}
}

func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ── 유틸 ────────────────────────────────────────────────────

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("JSON 인코딩 실패: %v", err)
		return []byte(`{}`)
	}
	return b
}

// cleanText 는 앞뒤 공백·제어문자를 없애고 글자 수를 제한한다.
func cleanText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	const hexd = "0123456789abcdef"
	out := make([]byte, 12)
	for i, v := range b {
		out[i*2] = hexd[v>>4]
		out[i*2+1] = hexd[v&0x0f]
	}
	return string(out)
}

func randDigits(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = '0' + b[i]%10
	}
	return string(b)
}
