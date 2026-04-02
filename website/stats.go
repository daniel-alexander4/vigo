// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// --- Stats Store ---

// StatsStore persists request data in SQLite.
type StatsStore struct {
	db *sql.DB
	mu sync.Mutex // serialize writes
}

// NewStatsStore opens (or creates) the SQLite database at the given path.
func NewStatsStore(dbPath string) (*StatsStore, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening stats db: %w", err)
	}

	// Create tables.
	schema := `
	CREATE TABLE IF NOT EXISTS page_views (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		path       TEXT    NOT NULL,
		referrer   TEXT    NOT NULL DEFAULT '',
		user_agent TEXT    NOT NULL DEFAULT '',
		ip_hash    TEXT    NOT NULL DEFAULT '',
		country    TEXT    NOT NULL DEFAULT '',
		created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
	);
	CREATE INDEX IF NOT EXISTS idx_page_views_created ON page_views(created_at);
	CREATE INDEX IF NOT EXISTS idx_page_views_path    ON page_views(path);

	CREATE TABLE IF NOT EXISTS daily_stats (
		date       TEXT NOT NULL,
		path       TEXT NOT NULL,
		views      INTEGER NOT NULL DEFAULT 0,
		visitors   INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (date, path)
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("creating stats schema: %w", err)
	}

	return &StatsStore{db: db}, nil
}

// Record inserts a page view.
func (s *StatsStore) Record(path, referrer, userAgent, ipHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO page_views (path, referrer, user_agent, ip_hash) VALUES (?, ?, ?, ?)`,
		path, referrer, userAgent, ipHash,
	)
	if err != nil {
		log.Printf("stats: record error: %v", err)
	}
}

// Aggregate rolls up raw page_views into daily_stats and prunes old raw rows.
// Call this periodically (e.g., hourly).
func (s *StatsStore) Aggregate() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Roll up yesterday and earlier into daily_stats.
	today := time.Now().UTC().Format("2006-01-02")
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO daily_stats (date, path, views, visitors)
		SELECT date(created_at) AS d, path, COUNT(*) AS views, COUNT(DISTINCT ip_hash) AS visitors
		FROM page_views
		WHERE date(created_at) < ?
		GROUP BY d, path
	`, today)
	if err != nil {
		log.Printf("stats: aggregate error: %v", err)
		return
	}

	// Prune aggregated raw rows (keep today's for live stats).
	_, _ = s.db.Exec(`DELETE FROM page_views WHERE date(created_at) < ?`, today)
}

// --- Query types ---

type DailyStat struct {
	Date     string
	Views    int
	Visitors int
}

type PathStat struct {
	Path     string
	Views    int
	Visitors int
	Pct      float64 // percentage of total views
}

type ReferrerStat struct {
	Referrer string
	Views    int
	Pct      float64
}

type UACategory struct {
	Category string
	Count    int
	Pct      float64
}

type StatsData struct {
	// Summary
	TotalViews     int
	TotalVisitors  int
	TodayViews     int
	TodayVisitors  int
	AvgDailyViews  int
	Days           int

	// Time series
	DailyStats []DailyStat
	SparkSVG   template.HTML

	// Breakdowns
	TopPages    []PathStat
	TopReferrers []ReferrerStat
	UABreakdown  []UACategory

	// Period
	PeriodDays int
	PeriodLabel string
}

// Query returns stats for the given number of days.
func (s *StatsStore) Query(days int) StatsData {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	today := time.Now().UTC().Format("2006-01-02")

	data := StatsData{
		PeriodDays:  days,
		PeriodLabel: fmt.Sprintf("Last %d days", days),
	}

	// Daily totals from aggregated + today's live data.
	dailyMap := make(map[string]DailyStat)

	// From daily_stats table.
	rows, err := s.db.Query(`
		SELECT date, SUM(views), SUM(visitors)
		FROM daily_stats WHERE date >= ? GROUP BY date ORDER BY date
	`, cutoff)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ds DailyStat
			rows.Scan(&ds.Date, &ds.Views, &ds.Visitors)
			dailyMap[ds.Date] = ds
		}
	}

	// Add today's live data from raw page_views.
	var todayViews, todayVisitors int
	s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT ip_hash) FROM page_views WHERE date(created_at) = ?`, today).Scan(&todayViews, &todayVisitors)
	if todayViews > 0 {
		dailyMap[today] = DailyStat{Date: today, Views: todayViews, Visitors: todayVisitors}
	}
	data.TodayViews = todayViews
	data.TodayVisitors = todayVisitors

	// Build sorted daily series (fill gaps with zeros).
	start, _ := time.Parse("2006-01-02", cutoff)
	end := time.Now().UTC()
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		ds := d.Format("2006-01-02")
		if v, ok := dailyMap[ds]; ok {
			data.DailyStats = append(data.DailyStats, v)
		} else {
			data.DailyStats = append(data.DailyStats, DailyStat{Date: ds})
		}
	}

	// Totals.
	for _, ds := range data.DailyStats {
		data.TotalViews += ds.Views
		data.TotalVisitors += ds.Visitors
	}
	data.Days = len(data.DailyStats)
	if data.Days > 0 {
		data.AvgDailyViews = data.TotalViews / data.Days
	}

	// Generate sparkline SVG.
	data.SparkSVG = generateSparkline(data.DailyStats, 600, 120)

	// Top pages (combined aggregated + today).
	data.TopPages = s.queryTopPages(cutoff, today)
	// Top referrers.
	data.TopReferrers = s.queryTopReferrers(cutoff, today)
	// UA breakdown.
	data.UABreakdown = s.queryUABreakdown(cutoff, today)

	return data
}

func (s *StatsStore) queryTopPages(cutoff, today string) []PathStat {
	pathMap := make(map[string]*PathStat)

	// Aggregated.
	rows, _ := s.db.Query(`SELECT path, SUM(views), SUM(visitors) FROM daily_stats WHERE date >= ? GROUP BY path`, cutoff)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ps PathStat
			rows.Scan(&ps.Path, &ps.Views, &ps.Visitors)
			pathMap[ps.Path] = &ps
		}
	}

	// Today's live.
	rows2, _ := s.db.Query(`SELECT path, COUNT(*), COUNT(DISTINCT ip_hash) FROM page_views WHERE date(created_at) = ? GROUP BY path`, today)
	if rows2 != nil {
		defer rows2.Close()
		for rows2.Next() {
			var path string
			var views, visitors int
			rows2.Scan(&path, &views, &visitors)
			if ps, ok := pathMap[path]; ok {
				ps.Views += views
				ps.Visitors += visitors
			} else {
				pathMap[path] = &PathStat{Path: path, Views: views, Visitors: visitors}
			}
		}
	}

	var result []PathStat
	totalViews := 0
	for _, ps := range pathMap {
		totalViews += ps.Views
		result = append(result, *ps)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Views > result[j].Views })
	for i := range result {
		if totalViews > 0 {
			result[i].Pct = float64(result[i].Views) / float64(totalViews) * 100
		}
	}
	if len(result) > 20 {
		result = result[:20]
	}
	return result
}

func (s *StatsStore) queryTopReferrers(cutoff, today string) []ReferrerStat {
	refMap := make(map[string]int)

	// Aggregated — referrers are only in raw page_views, not in daily_stats.
	// For historical referrers we'd need a separate table. For now, just use
	// raw data that hasn't been pruned yet + today.
	rows, _ := s.db.Query(`
		SELECT referrer, COUNT(*) FROM page_views
		WHERE referrer != '' AND date(created_at) >= ?
		GROUP BY referrer ORDER BY COUNT(*) DESC LIMIT 20
	`, cutoff)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ref string
			var count int
			rows.Scan(&ref, &count)
			refMap[ref] += count
		}
	}

	var result []ReferrerStat
	total := 0
	for ref, count := range refMap {
		total += count
		result = append(result, ReferrerStat{Referrer: ref, Views: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Views > result[j].Views })
	for i := range result {
		if total > 0 {
			result[i].Pct = float64(result[i].Views) / float64(total) * 100
		}
	}
	return result
}

func (s *StatsStore) queryUABreakdown(cutoff, today string) []UACategory {
	catMap := make(map[string]int)

	rows, _ := s.db.Query(`
		SELECT user_agent, COUNT(*) FROM page_views
		WHERE date(created_at) >= ?
		GROUP BY user_agent
	`, cutoff)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var ua string
			var count int
			rows.Scan(&ua, &count)
			cat := classifyUA(ua)
			catMap[cat] += count
		}
	}

	var result []UACategory
	total := 0
	for cat, count := range catMap {
		total += count
		result = append(result, UACategory{Category: cat, Count: count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Count > result[j].Count })
	for i := range result {
		if total > 0 {
			result[i].Pct = float64(result[i].Count) / float64(total) * 100
		}
	}
	return result
}

// classifyUA buckets a user-agent string into a human-readable category.
func classifyUA(ua string) string {
	low := strings.ToLower(ua)
	switch {
	case strings.Contains(low, "googlebot"):
		return "Googlebot"
	case strings.Contains(low, "bingbot"):
		return "Bingbot"
	case strings.Contains(low, "yandex"):
		return "Yandex"
	case strings.Contains(low, "baiduspider"):
		return "Baidu"
	case strings.Contains(low, "duckduckbot"):
		return "DuckDuckBot"
	case strings.Contains(low, "slurp"):
		return "Yahoo"
	case strings.Contains(low, "semrush"):
		return "SEMrush"
	case strings.Contains(low, "ahrefs"):
		return "Ahrefs"
	case strings.Contains(low, "bot") || strings.Contains(low, "crawler") || strings.Contains(low, "spider"):
		return "Other bots"
	case strings.Contains(low, "curl") || strings.Contains(low, "wget") || strings.Contains(low, "httpie"):
		return "CLI tools"
	case strings.Contains(low, "chrome") && !strings.Contains(low, "edg"):
		return "Chrome"
	case strings.Contains(low, "firefox"):
		return "Firefox"
	case strings.Contains(low, "safari") && !strings.Contains(low, "chrome"):
		return "Safari"
	case strings.Contains(low, "edg"):
		return "Edge"
	case ua == "":
		return "No UA"
	default:
		return "Other"
	}
}

// --- Sparkline SVG ---

func generateSparkline(daily []DailyStat, width, height int) template.HTML {
	if len(daily) == 0 {
		return ""
	}

	// Extract values.
	values := make([]float64, len(daily))
	maxVal := 0.0
	for i, ds := range daily {
		values[i] = float64(ds.Views)
		if values[i] > maxVal {
			maxVal = values[i]
		}
	}
	if maxVal == 0 {
		maxVal = 1
	}

	padTop := 10.0
	padBottom := 25.0
	padLeft := 45.0
	padRight := 10.0
	plotW := float64(width) - padLeft - padRight
	plotH := float64(height) - padTop - padBottom

	// Build SVG.
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" style="width:100%%;max-width:%dpx;height:auto;">`, width, height, width)

	// Y-axis gridlines and labels.
	niceMax := niceNum(maxVal)
	steps := 4
	for i := 0; i <= steps; i++ {
		val := niceMax * float64(i) / float64(steps)
		y := padTop + plotH - (val/niceMax)*plotH
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#e5e7eb" stroke-width="0.5"/>`, padLeft, y, float64(width)-padRight, y)
		label := fmt.Sprintf("%.0f", val)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="10" fill="#6b7280" text-anchor="end" dominant-baseline="middle">%s</text>`, padLeft-6, y, label)
	}

	// Area fill.
	fmt.Fprintf(&b, `<path d="M%.1f,%.1f`, padLeft, padTop+plotH)
	for i, v := range values {
		x := padLeft + float64(i)/float64(len(values)-1)*plotW
		y := padTop + plotH - (v/niceMax)*plotH
		fmt.Fprintf(&b, ` L%.1f,%.1f`, x, y)
	}
	fmt.Fprintf(&b, ` L%.1f,%.1f Z" fill="rgba(37,99,235,0.1)" stroke="none"/>`, padLeft+plotW, padTop+plotH)

	// Line.
	fmt.Fprintf(&b, `<polyline points="`)
	for i, v := range values {
		x := padLeft + float64(i)/float64(len(values)-1)*plotW
		y := padTop + plotH - (v/niceMax)*plotH
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
	}
	fmt.Fprintf(&b, `" fill="none" stroke="#2563eb" stroke-width="1.5" stroke-linejoin="round"/>`)

	// X-axis labels (first, middle, last dates).
	if len(daily) > 0 {
		labels := []struct {
			idx int
			x   float64
			anchor string
		}{
			{0, padLeft, "start"},
			{len(daily) / 2, padLeft + plotW/2, "middle"},
			{len(daily) - 1, padLeft + plotW, "end"},
		}
		for _, l := range labels {
			if l.idx < len(daily) {
				d := daily[l.idx].Date
				if len(d) >= 10 {
					d = d[5:] // MM-DD
				}
				fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="10" fill="#6b7280" text-anchor="%s">%s</text>`, l.x, padTop+plotH+16, l.anchor, d)
			}
		}
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// niceNum rounds up to a "nice" number for axis scaling.
func niceNum(v float64) float64 {
	if v <= 0 {
		return 1
	}
	exp := math.Floor(math.Log10(v))
	frac := v / math.Pow(10, exp)
	var nice float64
	switch {
	case frac <= 1.5:
		nice = 1
	case frac <= 3:
		nice = 2
	case frac <= 7:
		nice = 5
	default:
		nice = 10
	}
	return nice * math.Pow(10, exp)
}

// --- Middleware ---

// statsMiddleware wraps an http.Handler and records page views.
// Skips static assets and the stats page itself.
func statsMiddleware(next http.Handler, store *StatsStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		// Don't record static assets, favicon, or the stats page.
		path := r.URL.Path
		if strings.HasPrefix(path, "/static/") || path == "/favicon.ico" || strings.HasPrefix(path, "/stats") {
			return
		}

		// Hash the IP — never store raw IPs.
		ip := r.RemoteAddr
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
		}
		hash := sha256.Sum256([]byte(ip + time.Now().UTC().Format("2006-01-02")))
		ipHash := hex.EncodeToString(hash[:8]) // daily-rotating hash

		referrer := r.Header.Get("Referer")
		// Strip query params from referrer for privacy.
		if idx := strings.Index(referrer, "?"); idx > 0 {
			referrer = referrer[:idx]
		}

		ua := r.Header.Get("User-Agent")
		if len(ua) > 256 {
			ua = ua[:256]
		}

		go store.Record(path, referrer, ua, ipHash)
	})
}

// --- Auth ---

// basicAuth wraps a handler with HTTP Basic Auth.
func basicAuth(next http.HandlerFunc, user, pass string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 || subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Vigo Stats"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// --- Horizontal bar chart SVG ---

func barChartSVG(items []barItem, width, maxBars int) template.HTML {
	if len(items) == 0 {
		return template.HTML(`<p style="color:#6b7280;font-size:14px;">No data yet</p>`)
	}
	if len(items) > maxBars {
		items = items[:maxBars]
	}

	maxVal := 0
	for _, item := range items {
		if item.Value > maxVal {
			maxVal = item.Value
		}
	}
	if maxVal == 0 {
		maxVal = 1
	}

	barH := 28
	gap := 4
	labelW := 200
	valueW := 60
	barW := width - labelW - valueW - 20
	totalH := len(items) * (barH + gap)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" style="width:100%%;max-width:%dpx;height:auto;">`, width, totalH, width)

	for i, item := range items {
		y := i * (barH + gap)
		w := float64(item.Value) / float64(maxVal) * float64(barW)
		if w < 2 {
			w = 2
		}

		// Label.
		label := item.Label
		if len(label) > 30 {
			label = label[:27] + "..."
		}
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="12" fill="#1a202c" dominant-baseline="middle">%s</text>`,
			0, y+barH/2, template.HTMLEscapeString(label))

		// Bar.
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%.1f" height="%d" rx="3" fill="#2563eb" opacity="0.75"/>`,
			labelW, y+2, w, barH-4)

		// Value.
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="11" fill="#6b7280" dominant-baseline="middle">%d (%.0f%%)</text>`,
			labelW+barW+8, y+barH/2, item.Value, item.Pct)
	}

	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

type barItem struct {
	Label string
	Value int
	Pct   float64
}
