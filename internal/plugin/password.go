package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	rayleabot "github.com/RayleaBot/RayleaBot/sdk/go"
)

const (
	defaultPasswordAPIURL        = "https://db.18183.com/sjzmm/data/daily/{date}.json"
	defaultPasswordCacheMinutes  = 30
	defaultPasswordTimeoutSecond = 8
	maxPasswordResponseBytes     = 512 * 1024
)

var chinaLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

var expectedPasswordMaps = []string{"零号大坝", "长弓溪谷", "巴克什", "航天基地", "潮汐监狱", "AZ3核电站"}

type passwordSettings struct {
	APIURL        string
	CacheDuration time.Duration
	Timeout       int
}

type passwordEntry struct {
	MapName  string `json:"map_name"`
	Password string `json:"password"`
	Location string `json:"location"`
}

type passwordRecord struct {
	Date        string          `json:"date"`
	UpdateLabel string          `json:"update_label"`
	FetchedAt   string          `json:"fetched_at"`
	UpdatedAt   string          `json:"updated_at"`
	Source      string          `json:"source"`
	Entries     []passwordEntry `json:"entries"`
	Cached      bool            `json:"-"`
}

type dailyPasswordEnvelope struct {
	Date       string            `json:"date"`
	UpdatedAt  string            `json:"updatedAt"`
	SourceMode string            `json:"sourceMode"`
	Entries    map[string]string `json:"entries"`
}

type tminiEnvelope struct {
	Status  string    `json:"status"`
	Message string    `json:"message"`
	Data    tminiData `json:"data"`
}

type tminiData struct {
	UpdateDate  string          `json:"update_date"`
	TotalCount  int             `json:"total_count"`
	Passwords   []tminiPassword `json:"passwords"`
	Source      string          `json:"source"`
	LastUpdated string          `json:"last_updated"`
}

type tminiPassword struct {
	MapName      string            `json:"map_name"`
	Password     string            `json:"password"`
	LocationInfo tminiLocationInfo `json:"location_info"`
}

type tminiLocationInfo struct {
	Description string `json:"description"`
}

type passwordService struct {
	actions  hostActions
	settings passwordSettings
	now      func() time.Time
}

func newPasswordService(actions hostActions, config map[string]any, now func() time.Time) (*passwordService, error) {
	settings, err := readPasswordSettings(config)
	if err != nil {
		return nil, err
	}
	return &passwordService{actions: actions, settings: settings, now: now}, nil
}

func readPasswordSettings(config map[string]any) (passwordSettings, error) {
	apiURL := stringSetting(config, "password_api_url", defaultPasswordAPIURL)
	validationURL := strings.ReplaceAll(apiURL, "{date}", "2000-01-02")
	parsed, err := url.Parse(validationURL)
	if err != nil || parsed.Scheme != "https" || strings.TrimSpace(parsed.Host) == "" || parsed.User != nil {
		return passwordSettings{}, fmt.Errorf("password_api_url must be an HTTPS URL without user information")
	}
	cacheMinutes := intSetting(config, "password_cache_minutes", defaultPasswordCacheMinutes)
	if cacheMinutes < 5 {
		cacheMinutes = 5
	}
	if cacheMinutes > 720 {
		cacheMinutes = 720
	}
	timeout := intSetting(config, "password_timeout_seconds", defaultPasswordTimeoutSecond)
	if timeout < 3 {
		timeout = 3
	}
	if timeout > 30 {
		timeout = 30
	}
	return passwordSettings{APIURL: apiURL, CacheDuration: time.Duration(cacheMinutes) * time.Minute, Timeout: timeout}, nil
}

func (service *passwordService) get(ctx context.Context) (passwordRecord, error) {
	now := service.now().In(chinaLocation)
	today := now.Format("2006-01-02")
	cached, found := service.loadCache(ctx, today)
	if found && cacheIsFresh(cached, now, service.settings.CacheDuration) {
		cached.Cached = true
		return cached, nil
	}

	fetched, err := service.fetch(ctx, now)
	if err != nil {
		if found {
			cached.Cached = true
			return cached, nil
		}
		return passwordRecord{}, err
	}
	if _, err := service.actions.KVSet(ctx, passwordCacheKey(today), fetched); err != nil {
		service.log(ctx, "warn", "今日密码已获取，但缓存保存失败。", map[string]any{"date": today, "error": err.Error()})
	}
	return fetched, nil
}

func (service *passwordService) fetch(ctx context.Context, now time.Time) (passwordRecord, error) {
	today := now.In(chinaLocation).Format("2006-01-02")
	requestURL := strings.ReplaceAll(service.settings.APIURL, "{date}", today)
	result, err := service.actions.HTTPRequest(ctx, httpRequest{
		Method: "GET",
		URL:    requestURL,
		Headers: map[string]string{
			"Accept":     "application/json",
			"User-Agent": "RayleaBot delta-force-plugin/0.1",
		},
		TimeoutSeconds: service.settings.Timeout,
	})
	if err != nil {
		return passwordRecord{}, fmt.Errorf("request daily passwords: %w", err)
	}
	status := actionInt(result["status_code"])
	if status < 200 || status >= 300 {
		return passwordRecord{}, fmt.Errorf("daily password source returned HTTP %d", status)
	}
	body, err := actionBody(result)
	if err != nil {
		return passwordRecord{}, err
	}
	if len(body) == 0 || len(body) > maxPasswordResponseBytes {
		return passwordRecord{}, fmt.Errorf("daily password response size %d is outside the accepted range", len(body))
	}
	return decodePasswordResponse(body, now)
}

func decodePasswordResponse(body []byte, now time.Time) (passwordRecord, error) {
	var daily dailyPasswordEnvelope
	if err := json.Unmarshal(body, &daily); err != nil {
		return passwordRecord{}, fmt.Errorf("decode daily password response: %w", err)
	}
	if daily.Date != "" || daily.Entries != nil {
		return validateDailyPasswordResponse(daily, now)
	}

	var compatible tminiEnvelope
	if err := json.Unmarshal(body, &compatible); err != nil {
		return passwordRecord{}, fmt.Errorf("decode compatible daily password response: %w", err)
	}
	if compatible.Status != "success" {
		return passwordRecord{}, fmt.Errorf("daily password source returned an unsupported response")
	}
	return validateTminiPasswordResponse(compatible.Data, now)
}

func validateDailyPasswordResponse(data dailyPasswordEnvelope, now time.Time) (passwordRecord, error) {
	today := now.In(chinaLocation).Format("2006-01-02")
	if data.Date != today {
		return passwordRecord{}, fmt.Errorf("daily password date %q does not match %s", data.Date, today)
	}
	updatedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(data.UpdatedAt))
	if err != nil || updatedAt.In(chinaLocation).Format("2006-01-02") != today {
		return passwordRecord{}, fmt.Errorf("daily password updatedAt is not from %s in Beijing time", today)
	}
	candidates := make([]passwordEntry, 0, len(data.Entries))
	for mapName, password := range data.Entries {
		candidates = append(candidates, passwordEntry{MapName: mapName, Password: password})
	}
	entries, err := validatePasswordEntries(candidates)
	if err != nil {
		return passwordRecord{}, err
	}
	return passwordRecord{
		Date:        today,
		UpdateLabel: now.In(chinaLocation).Format("01月02日"),
		FetchedAt:   now.In(chinaLocation).Format(time.RFC3339),
		UpdatedAt:   updatedAt.In(chinaLocation).Format("2006-01-02 15:04:05"),
		Source:      "18183 每日密码",
		Entries:     entries,
	}, nil
}

func validateTminiPasswordResponse(data tminiData, now time.Time) (passwordRecord, error) {
	today := now.In(chinaLocation).Format("2006-01-02")
	dateLabel := now.In(chinaLocation).Format("01月02日")
	if !strings.Contains(data.UpdateDate, dateLabel) {
		return passwordRecord{}, fmt.Errorf("daily password update label %q does not match %s", data.UpdateDate, dateLabel)
	}
	updatedAt, err := time.ParseInLocation("2006-01-02 15:04:05", strings.TrimSpace(data.LastUpdated), chinaLocation)
	if err != nil || updatedAt.Format("2006-01-02") != today {
		return passwordRecord{}, fmt.Errorf("daily password last_updated is not from %s", today)
	}
	candidates := make([]passwordEntry, 0, len(data.Passwords))
	for _, candidate := range data.Passwords {
		candidates = append(candidates, passwordEntry{
			MapName:  candidate.MapName,
			Password: candidate.Password,
			Location: candidate.LocationInfo.Description,
		})
	}
	entries, err := validatePasswordEntries(candidates)
	if err != nil {
		return passwordRecord{}, err
	}
	source := truncateRunes(strings.TrimSpace(data.Source), 80)
	if source == "" {
		source = "兼容每日密码源"
	}
	return passwordRecord{
		Date:        today,
		UpdateLabel: strings.TrimSpace(data.UpdateDate),
		FetchedAt:   now.In(chinaLocation).Format(time.RFC3339),
		UpdatedAt:   updatedAt.Format("2006-01-02 15:04:05"),
		Source:      source,
		Entries:     entries,
	}, nil
}

func validatePasswordEntries(candidates []passwordEntry) ([]passwordEntry, error) {
	if len(candidates) != len(expectedPasswordMaps) {
		return nil, fmt.Errorf("daily password source returned %d maps; expected %d", len(candidates), len(expectedPasswordMaps))
	}
	entries := make([]passwordEntry, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		mapName := canonicalMapName(candidate.MapName)
		if mapName == "" {
			return nil, fmt.Errorf("daily password source returned an empty map name")
		}
		if _, exists := seen[mapName]; exists {
			return nil, fmt.Errorf("daily password source duplicated map %q", mapName)
		}
		seen[mapName] = struct{}{}
		if len(candidate.Password) != 4 {
			return nil, fmt.Errorf("map %q password is not four digits", mapName)
		}
		if _, err := strconv.Atoi(candidate.Password); err != nil {
			return nil, fmt.Errorf("map %q password is not four digits", mapName)
		}
		entries = append(entries, passwordEntry{
			MapName:  mapName,
			Password: candidate.Password,
			Location: truncateRunes(strings.TrimSpace(candidate.Location), 180),
		})
	}
	for _, required := range expectedPasswordMaps {
		if _, exists := seen[required]; !exists {
			return nil, fmt.Errorf("daily password source is missing %q", required)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return passwordMapOrder(entries[i].MapName) < passwordMapOrder(entries[j].MapName)
	})
	return entries, nil
}

func (service *passwordService) loadCache(ctx context.Context, today string) (passwordRecord, bool) {
	result, err := service.actions.KVGet(ctx, passwordCacheKey(today))
	if err != nil || result["exists"] != true {
		return passwordRecord{}, false
	}
	raw, err := json.Marshal(result["value"])
	if err != nil {
		return passwordRecord{}, false
	}
	var record passwordRecord
	if json.Unmarshal(raw, &record) != nil || record.Date != today || len(record.Entries) != len(expectedPasswordMaps) {
		return passwordRecord{}, false
	}
	if _, err := validatePasswordEntries(record.Entries); err != nil {
		return passwordRecord{}, false
	}
	return record, true
}

func (service *passwordService) log(ctx context.Context, level, message string, fields map[string]any) {
	// Logging is best-effort: a logger failure must not hide a valid password result.
	_, _ = service.actions.LoggerWrite(ctx, rayleabot.LoggerWriteRequest{Level: level, Message: message, Fields: fields})
}

func passwordCacheKey(date string) string {
	return "passwords:" + date
}

func cacheIsFresh(record passwordRecord, now time.Time, duration time.Duration) bool {
	fetchedAt, err := time.Parse(time.RFC3339, record.FetchedAt)
	if err != nil {
		return false
	}
	age := now.Sub(fetchedAt.In(chinaLocation))
	return age >= 0 && age < duration
}

func canonicalMapName(value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToUpper(value) {
	case "AZ3", "AZ3核电站":
		return "AZ3核电站"
	default:
		return value
	}
}

func passwordMapOrder(name string) int {
	for index, expected := range expectedPasswordMaps {
		if name == expected {
			return index
		}
	}
	return len(expectedPasswordMaps) + 1
}

func actionBody(result rayleabot.ActionResult) ([]byte, error) {
	if text, ok := result["body_text"].(string); ok && text != "" {
		return []byte(text), nil
	}
	encoded, _ := result["body_base64"].(string)
	if encoded == "" {
		return nil, fmt.Errorf("daily password source returned an empty body")
	}
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode daily password response body: %w", err)
	}
	return body, nil
}

func actionInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	default:
		return 0
	}
}

func stringSetting(config map[string]any, key, fallback string) string {
	value, _ := config[key].(string)
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func intSetting(config map[string]any, key string, fallback int) int {
	value, exists := config[key]
	if !exists {
		return fallback
	}
	parsed := actionInt(value)
	if parsed == 0 {
		return fallback
	}
	return parsed
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "…"
}

func passwordRenderData(record passwordRecord) map[string]any {
	entries := make([]map[string]any, 0, len(record.Entries))
	for index, candidate := range record.Entries {
		entries = append(entries, map[string]any{
			"index":    fmt.Sprintf("%02d", index+1),
			"map_name": candidate.MapName,
			"password": candidate.Password,
			"location": candidate.Location,
		})
	}
	cacheLabel := "实时校验"
	if record.Cached {
		cacheLabel = "今日缓存"
	}
	return map[string]any{
		"title":        "每日密码",
		"date":         record.Date,
		"update_label": record.UpdateLabel,
		"updated_at":   record.UpdatedAt,
		"source":       record.Source,
		"cache_label":  cacheLabel,
		"cached":       record.Cached,
		"entries":      entries,
		"count":        len(entries),
	}
}

func formatPasswords(record passwordRecord) string {
	lines := []string{"三角洲每日密码 · " + record.Date}
	for _, candidate := range record.Entries {
		line := candidate.MapName + "：" + candidate.Password
		if candidate.Location != "" {
			line += " · " + candidate.Location
		}
		lines = append(lines, line)
	}
	status := "实时校验"
	if record.Cached {
		status = "今日缓存"
	}
	lines = append(lines, "数据状态："+status+"；来源："+record.Source+"；更新时间："+record.UpdatedAt)
	return strings.Join(lines, "\n")
}
