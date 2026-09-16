// vector-soccer — 같은 교내망에 붙은 학생들이 QR 로 접속해 1:1 로 겨루는 "빨대 축구".
// 중학교 1학년 과학 '알짜힘' 단원의 빨대 축구를 2인 대전 게임으로 옮겼다.
//
// 교사 PC 에서 이 한 개 exe 를 더블클릭하면:
//
//	① LAN IP 를 탐지하고 0.0.0.0 에 바인딩(학생이 붙을 수 있게 — 핵심).
//	② 기본 브라우저로 교사 화면(/host)을 자동으로 연다 — 접속 QR · 경기 현황 · 결과.
//	③ 학생은 QR 을 찍어 / 로 들어와 학번·이름을 넣고, 방을 만들거나 친구를 초대해 겨룬다.
//
// 물리는 서버가 계산한다(단일 진실원천). 웹 자산은 //go:embed 로 바이너리에 박혀 있어 배포 파일은 exe 하나.
//
// 빌드(리눅스에서 윈도우 exe, CGO 불필요):  ./build.sh
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed assets
var assetsFS embed.FS

const (
	preferredPort = 8090 // classroom-quiz(8080)와 동시에 켜도 겹치지 않게
	portTries     = 12
)

func main() {
	setupLogging()

	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		log.Fatalf("내장 자산 마운트 실패: %v", err)
	}

	primaryIP, candidates := detectLANIP()
	ln, port, err := listenLAN(preferredPort)
	if err != nil {
		log.Fatalf("포트 열기 실패: %v", err)
	}
	defer ln.Close()

	hub := newHub()
	hub.comp = loadOpenCompetition()
	go hub.run()

	joinURL := fmt.Sprintf("http://%s:%d/", primaryIP, port)
	media := networkMedia(primaryIP)
	info := map[string]any{
		"joinURL": joinURL, "port": port, "primaryIP": primaryIP,
		"candidates": candidates, "media": media,
	}

	mux := http.NewServeMux()

	fileServer := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			serveAsset(w, sub, "student.html")
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	// 교사 화면 + 결과 API — 교사 PC(localhost) 전용.
	mux.HandleFunc("/host", localOnly(func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, sub, "host.html")
	}))
	mux.HandleFunc("GET /api/results", localOnly(handleListResults))
	mux.HandleFunc("GET /api/results.csv", localOnly(handleResultsCSV))
	mux.HandleFunc("DELETE /api/results/{date}/{file}", localOnly(handleDeleteResult))

	mux.HandleFunc("/qr.png", func(w http.ResponseWriter, r *http.Request) {
		size := 320
		if s := r.URL.Query().Get("size"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n >= 128 && n <= 1280 {
				size = n
			}
		}
		png, err := qrcode.Encode(joinURL, qrcode.Medium, size)
		if err != nil {
			http.Error(w, "qr error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)
	})

	mux.HandleFunc("/info", localOnly(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	}))

	// WebSocket: ?role=host(교사 PC 전용)  또는  ?name=..&sid=..&token=..
	mux.HandleFunc("/ws", hub.serveWS)

	log.Printf("학생접속 %s  교사화면 http://127.0.0.1:%d/host  (연결: %s)", joinURL, port, media)
	if len(candidates) > 1 {
		log.Printf("LAN IP 후보 %v (QR 가 %s 로 안 되면 다른 후보로 시도)", candidates, primaryIP)
	}

	if os.Getenv("VS_NO_BROWSER") == "" {
		go openBrowser(fmt.Sprintf("http://127.0.0.1:%d/host", port))
	}

	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil {
		log.Printf("서버 종료: %v", err)
	}
}

func serveAsset(w http.ResponseWriter, sub fs.FS, name string) {
	b, err := fs.ReadFile(sub, name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}

// listenLAN 은 0.0.0.0(전체 인터페이스)에 바인딩한다 — 학생이 LAN 으로 붙으려면 필수.
func listenLAN(start int) (net.Listener, int, error) {
	var lastErr error
	for p := start; p < start+portTries; p++ {
		ln, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(p))
		if err == nil {
			return ln, p, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

// detectLANIP 은 사설망 IPv4 를 골라 반환한다(우선순위 192.168 > 10 > 172.16-31).
func detectLANIP() (string, []string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1", []string{"127.0.0.1"}
	}
	var cands []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			cands = append(cands, ip.String())
		}
	}
	if len(cands) == 0 {
		return "127.0.0.1", []string{"127.0.0.1"}
	}
	return pickPrimary(cands), cands
}

func pickPrimary(cands []string) string {
	rank := func(ip string) int {
		switch {
		case len(ip) >= 8 && ip[:8] == "192.168.":
			return 0
		case len(ip) >= 3 && ip[:3] == "10.":
			return 1
		default:
			return 2
		}
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if rank(c) < rank(best) {
			best = c
		}
	}
	return best
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("브라우저 자동 열기 실패(수동으로 %s 접속): %v", url, err)
	}
}

// setupLogging 은 exe 옆에 vector-soccer.log 를 남긴다 + 콘솔이 있으면 화면에도 출력.
func setupLogging() {
	log.SetFlags(log.LstdFlags)
	f, err := os.OpenFile(filepath.Join(exeDir(), "vector-soccer.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}
