package main

import (
	"encoding/json"
	"net"
	"net/http"
)

// 교사 전용 API. 학생도 같은 AP 에서 서버에 닿으므로 결과 열람·삭제는
// 반드시 교사 PC 본인(localhost)에서만 허용한다(classroom-quiz 의 localOnly 패턴).

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func localOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			http.Error(w, "forbidden — 교사 화면은 교사 PC 에서만 열 수 있습니다.", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func handleListResults(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date != "" && !validName(date) {
		writeErr(w, http.StatusBadRequest, "잘못된 날짜")
		return
	}
	list, err := listResults(date)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dates, _ := listDates()
	writeJSON(w, http.StatusOK, map[string]any{"results": list, "dates": dates})
}

func handleResultsCSV(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date != "" && !validName(date) {
		writeErr(w, http.StatusBadRequest, "잘못된 날짜")
		return
	}
	list, err := listResults(date)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := "빨대축구-전체.csv"
	if date != "" {
		name = "빨대축구-" + date + ".csv"
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlPathEscape(name))
	_, _ = w.Write(resultsCSV(list))
}

func handleDeleteResult(w http.ResponseWriter, r *http.Request) {
	if err := deleteResult(r.PathValue("date"), r.PathValue("file")); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func urlPathEscape(s string) string {
	const hexd = "0123456789ABCDEF"
	out := make([]byte, 0, len(s)*3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' {
			out = append(out, c)
		} else {
			out = append(out, '%', hexd[c>>4], hexd[c&15])
		}
	}
	return string(out)
}
