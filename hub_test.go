package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// 허브를 고루틴 없이 직접 구동한다(onRegister/onMessage/onTick 을 순서대로 호출).

type fakeHub struct {
	*Hub
	clock time.Time
	saved []*Match
}

func newFakeHub() *fakeHub {
	f := &fakeHub{Hub: newHub(), clock: t0}
	f.now = func() time.Time { return f.clock }
	f.save = func(m *Match) { f.saved = append(f.saved, m) }
	return f
}

func (f *fakeHub) join(token, name string) *client {
	c := &client{hub: f.Hub, send: make(chan []byte, 1024), token: token, name: name}
	f.onRegister(c)
	return c
}

func (f *fakeHub) say(c *client, v any) {
	b, _ := json.Marshal(v)
	f.onMessage(inMsg{c: c, data: b})
}

func (f *fakeHub) advance(d time.Duration) {
	for el := time.Duration(0); el < d; el += tickPeriod {
		f.clock = f.clock.Add(tickPeriod)
		f.onTick()
	}
}

// last 는 c 가 받은 메시지 중 타입 t 의 마지막 것을 돌려준다(버퍼는 비운다).
func last(c *client, t string) map[string]any {
	var got map[string]any
	for {
		select {
		case b, ok := <-c.send:
			if !ok {
				return got
			}
			var m map[string]any
			if json.Unmarshal(b, &m) == nil && m["t"] == t {
				got = m
			}
		default:
			return got
		}
	}
}

func TestRoomCodeMatch(t *testing.T) {
	f := newFakeHub()
	a, b := f.join("ta", "민수"), f.join("tb", "지우")
	f.say(a, map[string]any{"t": "create", "rules": Rules{TimeSec: 60}})
	f.advance(200 * time.Millisecond)
	lobby := last(b, "lobby")
	rooms := lobby["rooms"].([]any)
	if len(rooms) != 1 {
		t.Fatalf("열린 방 1개가 보여야 함: %v", lobby)
	}
	code := rooms[0].(map[string]any)["code"].(string)
	f.say(b, map[string]any{"t": "join", "code": code})
	if ma, mb := last(a, "match"), last(b, "match"); ma == nil || mb == nil || ma["side"].(float64) != 0 || mb["side"].(float64) != 1 {
		t.Fatalf("두 학생 모두 경기 시작 메시지를 받아야 함: %v / %v", ma, mb)
	}
	if len(f.rooms) != 0 {
		t.Fatal("경기가 시작되면 방은 닫힌다")
	}
}

func TestInviteFlowNotRecorded(t *testing.T) {
	f := newFakeHub()
	a, b := f.join("ta", "민수"), f.join("tb", "지우")
	pb := f.players["tb"]
	pa := f.players["ta"]
	f.say(a, map[string]any{"t": "invite", "to": pb.id, "rules": Rules{Goals: 1}})
	f.advance(200 * time.Millisecond)
	invs := last(b, "lobby")["invites"].([]any)
	if len(invs) != 1 {
		t.Fatal("초대가 도착해야 함")
	}
	f.say(b, map[string]any{"t": "inviteReply", "from": pa.id, "accept": true})
	m := pa.match
	if m == nil || pb.match != m {
		t.Fatal("수락하면 경기 시작")
	}
	if m.sideOf(pb) != 1 || m.kickOffSide() != 1 {
		t.Fatal("초대 받은 사람이 선축")
	}
	f.advance(ReadyDur + 100*time.Millisecond)
	m.ball, m.vel = vec{97, 30}, vec{30, 0}
	f.advance(500 * time.Millisecond)
	if m.phase != phEnd || len(f.saved) != 0 {
		t.Fatalf("골 → 종료, 보조 경기장 경기는 기록 안 함: phase=%s saved=%d", m.phase, len(f.saved))
	}
	r := buildResult(m, f.clock)
	if r.Players[0].Result != "승" || r.Players[1].Result != "패" || r.Players[0].Goals != 1 {
		t.Fatalf("결과 기록: %+v", r.Players)
	}
	if st := last(a, "s"); st == nil || st["ph"] != "end" {
		t.Fatalf("종료 상태 수신: %v", st)
	}
	// 다시 하기 — 둘 다 누르면 진영을 바꿔 새 경기
	f.say(a, map[string]any{"t": "rematch"})
	if st := last(b, "s"); st == nil || st["rematch"].([]any)[0] != true {
		t.Fatalf("상대 화면에 다시 하기 요청이 보여야 함: %v", st)
	}
	f.say(b, map[string]any{"t": "rematch"})
	if pa.match == nil || pa.match == m || pa.match.sideOf(pa) != 1 {
		t.Fatal("다시 하기 → 진영 바꿔 새 경기")
	}
	if nm := pa.match; nm.kickOffSide() != nm.sideOf(pb) {
		t.Fatal("다시 하기 선축은 직전 경기 진 사람(b)")
	}
}

func TestDisconnectWaitsForReturn(t *testing.T) {
	f := newFakeHub()
	a, b := f.join("ta", "민수"), f.join("tb", "지우")
	f.say(a, map[string]any{"t": "create", "rules": Rules{TimeSec: 60}})
	f.say(b, map[string]any{"t": "join", "code": f.players["ta"].room.code})
	m := f.players["ta"].match
	f.advance(ReadyDur + time.Second)
	left := m.playLeft
	f.onUnregister(b)
	f.advance(3 * time.Minute)
	if m.phase != phPause || m.playLeft != left {
		t.Fatalf("끊기면 돌아올 때까지 멈춤(시간도 멈춤): phase=%s", m.phase)
	}
	// 같은 토큰으로 재접속 → 카운트다운 후 재개
	b2 := f.join("tb", "지우")
	if m.phase != phReady || last(b2, "match") == nil {
		t.Fatalf("재접속하면 경기 화면 복귀: %s", m.phase)
	}
	f.advance(ReadyDur + 100*time.Millisecond)
	// 브라우저를 닫고 새 토큰으로 들어와도 같은 학번·이름이면 복귀
	f.onUnregister(b2)
	b3 := f.join("tb-new", "지우")
	if m.phase != phReady || f.players["tb-new"] != m.players[1] || last(b3, "match") == nil {
		t.Fatal("새 연결이어도 같은 학생으로 경기 복귀")
	}
	// 기다리다 그만두면 기록 없이 취소
	f.advance(ReadyDur + 100*time.Millisecond)
	f.onUnregister(b3)
	last(a, "") // 버퍼 비우기
	f.say(a, map[string]any{"t": "leave"})
	if len(f.matches) != 0 || len(f.saved) != 0 || f.players["ta"].match != nil {
		t.Fatal("기다리던 쪽이 나가면 기록 없이 경기 취소")
	}
	if last(a, "cancelled") == nil {
		t.Fatal("취소 안내")
	}
}

func TestLeaveForfeits(t *testing.T) {
	f := newFakeHub()
	a, b := f.join("ta", "민수"), f.join("tb", "지우")
	f.say(a, map[string]any{"t": "create", "rules": Rules{TimeSec: 60}})
	f.say(b, map[string]any{"t": "join", "code": f.players["ta"].room.code})
	m := f.players["ta"].match
	f.advance(ReadyDur + time.Second)
	f.say(a, map[string]any{"t": "leave"})
	if m.phase != phEnd || m.winner != 1 || f.players["ta"].match != nil || f.players["tb"].match != m {
		t.Fatal("나가면 기권패, 상대는 결과 화면에 남는다")
	}
	f.say(b, map[string]any{"t": "leave"})
	if len(f.matches) != 0 {
		t.Fatal("둘 다 나가면 경기 정리")
	}
}

func TestOpponentCooldownHidden(t *testing.T) {
	f := newFakeHub()
	a, b := f.join("ta", "민수"), f.join("tb", "지우")
	f.say(a, map[string]any{"t": "create", "rules": Rules{TimeSec: 60}})
	f.say(b, map[string]any{"t": "join", "code": f.players["ta"].room.code})
	f.advance(ReadyDur + 100*time.Millisecond)
	f.say(a, map[string]any{"t": "blow", "dx": 1, "dy": 0}) // 방에 들어온 b 가 선축 — 무시됨
	f.say(b, map[string]any{"t": "blow", "dx": -1, "dy": 0})
	f.advance(100 * time.Millisecond)
	sa, sb := last(a, "s"), last(b, "s")
	if sb["cd"].(float64) <= 0 || sa["cd"].(float64) != 0 {
		t.Fatalf("선축이 먼저, 각자 자기 쿨타임만 받는다: a=%v b=%v", sa["cd"], sb["cd"])
	}
	f.say(b, map[string]any{"t": "blow", "dx": 1, "dy": 0}) // 헛방질
	f.advance(50 * time.Millisecond)
	if sb := last(b, "s"); sb["ct"].(float64) != float64((Cooldown + MissPenalty).Milliseconds()) {
		t.Fatalf("헛방질하면 전체 쿨타임이 늘어난다: %v", sb["ct"])
	}
}

func TestSubArenaClose(t *testing.T) {
	f := newFakeHub()
	a, b := f.join("ta", "민수"), f.join("tb", "지우")
	c, d := f.join("tc", "서연"), f.join("td", "하준")
	e := f.join("te", "도윤")
	f.say(a, map[string]any{"t": "create", "rules": Rules{TimeSec: 60}})
	f.say(b, map[string]any{"t": "join", "code": f.players["ta"].room.code})
	f.say(c, map[string]any{"t": "create", "rules": Rules{TimeSec: 60}})
	f.say(e, map[string]any{"t": "invite", "to": f.players["td"].id, "rules": Rules{TimeSec: 60}})
	f.advance(ReadyDur + time.Second)
	m := f.players["ta"].match
	if m == nil || len(f.rooms) != 1 || len(f.invites[f.players["td"]]) != 1 {
		t.Fatal("준비: 경기 1판 + 방 1개 + 초대 1개")
	}

	host := &client{hub: f.Hub, send: make(chan []byte, 1024), host: true}
	f.onRegister(host)
	f.say(host, map[string]any{"t": "subClose"})
	if len(f.matches) != 0 || len(f.rooms) != 0 || len(f.invites) != 0 || len(f.saved) != 0 {
		t.Fatalf("폐쇄하면 경기·방·초대가 기록 없이 사라짐: matches=%d rooms=%d invites=%d saved=%d",
			len(f.matches), len(f.rooms), len(f.invites), len(f.saved))
	}
	if f.players["ta"].match != nil || last(a, "cancelled") == nil || last(b, "cancelled") == nil {
		t.Fatal("경기하던 학생은 로비로")
	}
	f.say(c, map[string]any{"t": "create", "rules": Rules{TimeSec: 60}})
	if len(f.rooms) != 0 || last(c, "error") == nil {
		t.Fatal("닫혀 있는 동안은 방을 못 만든다")
	}
	f.say(e, map[string]any{"t": "invite", "to": f.players["td"].id, "rules": Rules{TimeSec: 60}})
	if len(f.invites) != 0 {
		t.Fatal("닫혀 있는 동안은 초대 못 함")
	}
	f.advance(200 * time.Millisecond)
	if l := last(d, "lobby"); l == nil || l["subClosed"] != true {
		t.Fatalf("학생 로비에 폐쇄 상태: %v", l)
	}

	f.say(host, map[string]any{"t": "subOpen"})
	f.say(c, map[string]any{"t": "create", "rules": Rules{TimeSec: 60}})
	if len(f.rooms) != 1 {
		t.Fatal("다시 열면 방을 만들 수 있다")
	}
}

func TestSubArenaCloseKeepsCompetition(t *testing.T) {
	f := newFakeHub()
	keys := []string{}
	for i, n := range []string{"민수", "지우"} {
		f.join(fmt.Sprintf("t%d", i), n)
		keys = append(keys, playerKey("", n))
	}
	host := &client{hub: f.Hub, send: make(chan []byte, 1024), host: true}
	f.onRegister(host)
	f.say(host, map[string]any{"t": "compCreate", "name": "리그", "type": "league", "rules": Rules{Goals: 1}, "keys": keys})
	f.say(host, map[string]any{"t": "compStartRound"})
	f.say(host, map[string]any{"t": "subClose"})
	if len(f.matches) != 1 {
		t.Fatal("주 경기장(대회 경기)은 폐쇄와 상관없이 계속")
	}
	var hm struct {
		Matches []struct {
			Fx    bool   `json:"fx"`
			Round string `json:"round"`
		} `json:"matches"`
		SubClosed bool `json:"subClosed"`
	}
	if json.Unmarshal(f.hostMsg(), &hm) != nil || len(hm.Matches) != 1 || !hm.Matches[0].Fx || hm.Matches[0].Round != "1라운드" || !hm.SubClosed {
		t.Fatalf("교사 화면: 대회 경기 표시·라운드 이름·폐쇄 상태: %+v", hm)
	}
	var m *Match
	for _, x := range f.matches {
		m = x
	}
	f.advance(ReadyDur + 100*time.Millisecond)
	m.ball, m.vel = vec{97, 30}, vec{30, 0}
	f.advance(500 * time.Millisecond)
	if m.phase != phEnd || len(f.saved) != 1 {
		t.Fatalf("대회 경기는 기록: phase=%s saved=%d", m.phase, len(f.saved))
	}
}

func TestResultsCSV(t *testing.T) {
	r := MatchResult{Date: "2026-09-16", FinishedAt: "2026-09-16T10:03:00+09:00", PlayedSec: 180,
		Rules: Rules{TimeSec: 180}, Reason: "time",
		Players: [2]ResultPlayer{{"10101", "민수", 2, 14, 3, "승"}, {"10102", "지우", 1, 11, 0, "패"}}}
	out := string(resultsCSV([]MatchResult{r}))
	want := "\xEF\xBB\xBF날짜,종료 시각,학번,이름,결과,득점,실점,바람 횟수,헛방질,상대 학번,상대 이름,경기 시간(초),종료 사유,규칙,대회\n" +
		"2026-09-16,10:03:00,10101,민수,승,2,1,14,3,10102,지우,180,시간 종료,3분,\n" +
		"2026-09-16,10:03:00,10102,지우,패,1,2,11,0,10101,민수,180,시간 종료,3분,\n"
	if out != want {
		t.Fatalf("CSV:\n%s", out)
	}
}

func TestBothDisconnectedIsNotRecorded(t *testing.T) {
	f := newFakeHub()
	a, b := f.join("ta", "민수"), f.join("tb", "지우")
	f.say(a, map[string]any{"t": "create", "rules": Rules{TimeSec: 60}})
	f.say(b, map[string]any{"t": "join", "code": f.players["ta"].room.code})
	f.advance(ReadyDur + time.Second)
	f.onUnregister(a)
	f.onUnregister(b)
	f.advance(time.Minute)
	if len(f.matches) != 1 {
		t.Fatal("둘 다 끊겨도 한동안은 기다린다")
	}
	for el := time.Duration(0); el < BothGoneWait; el += 10 * time.Second { // 빠르게 흘려보내기
		f.matches[f.players["ta"].match.id].phaseLeft += 10 * time.Second
		f.advance(tickPeriod)
		if len(f.matches) == 0 {
			break
		}
	}
	if len(f.matches) != 0 || len(f.saved) != 0 {
		t.Fatalf("둘 다 끊기면 기록 없이 정리: matches=%d saved=%d", len(f.matches), len(f.saved))
	}
	if f.players["ta"].match != nil {
		t.Fatal("학생의 경기 참조도 정리")
	}
}
