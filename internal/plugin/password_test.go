package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	rayleabot "github.com/RayleaBot/RayleaBot/sdk/go"
)

type fakeActions struct {
	httpResult  rayleabot.ActionResult
	httpErr     error
	httpCalls   int
	httpRequest httpRequest
	kv          map[string]any
}

func (fake *fakeActions) HTTPRequest(_ context.Context, request httpRequest) (rayleabot.ActionResult, error) {
	fake.httpCalls++
	fake.httpRequest = request
	return fake.httpResult, fake.httpErr
}

func (fake *fakeActions) KVGet(_ context.Context, key string) (rayleabot.ActionResult, error) {
	value, exists := fake.kv[key]
	return rayleabot.ActionResult{"exists": exists, "value": value}, nil
}

func (fake *fakeActions) KVSet(_ context.Context, key string, value any) (rayleabot.ActionResult, error) {
	if fake.kv == nil {
		fake.kv = map[string]any{}
	}
	fake.kv[key] = value
	return rayleabot.ActionResult{}, nil
}

func (*fakeActions) RenderImage(context.Context, rayleabot.RenderImageRequest) (rayleabot.ActionResult, error) {
	return rayleabot.ActionResult{}, nil
}

func (*fakeActions) LoggerWrite(context.Context, rayleabot.LoggerWriteRequest) (rayleabot.ActionResult, error) {
	return rayleabot.ActionResult{}, nil
}

func TestValidateDailyPasswordResponsePreservesLeadingZeroAndSortsMaps(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 30, 0, 0, chinaLocation)
	record, err := validateDailyPasswordResponse(validDailyPasswordData(now), now)
	if err != nil {
		t.Fatal(err)
	}
	if record.Date != "2026-09-03" || len(record.Entries) != 6 {
		t.Fatalf("unexpected record: %#v", record)
	}
	if record.Entries[0].MapName != "零号大坝" || record.Entries[3].Password != "0136" || record.Entries[5].MapName != "AZ3核电站" {
		t.Fatalf("map order or leading zero changed: %#v", record.Entries)
	}
}

func TestValidateDailyPasswordResponseRejectsStaleIncompleteAndInvalidData(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 30, 0, 0, chinaLocation)
	tests := []struct {
		name   string
		mutate func(*dailyPasswordEnvelope)
	}{
		{name: "stale date", mutate: func(data *dailyPasswordEnvelope) { data.Date = "2026-09-02" }},
		{name: "stale timestamp", mutate: func(data *dailyPasswordEnvelope) { data.UpdatedAt = "2026-09-02T15:59:59+08:00" }},
		{name: "missing map", mutate: func(data *dailyPasswordEnvelope) { delete(data.Entries, "长弓溪谷") }},
		{name: "duplicate alias", mutate: func(data *dailyPasswordEnvelope) { data.Entries["AZ3核电站"] = data.Entries["AZ3"] }},
		{name: "non digit password", mutate: func(data *dailyPasswordEnvelope) { data.Entries["巴克什"] = "12A4" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := validDailyPasswordData(now)
			test.mutate(&data)
			if _, err := validateDailyPasswordResponse(data, now); err == nil {
				t.Fatal("validateDailyPasswordResponse() error = nil")
			}
		})
	}
}

func TestDecodePasswordResponseAcceptsCompatibleTminiShape(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 30, 0, 0, chinaLocation)
	payload, err := json.Marshal(tminiEnvelope{Status: "success", Data: validTminiPasswordData(now)})
	if err != nil {
		t.Fatal(err)
	}
	record, err := decodePasswordResponse(payload, now)
	if err != nil {
		t.Fatal(err)
	}
	if record.Source != "兼容源" || len(record.Entries) != len(expectedPasswordMaps) {
		t.Fatalf("unexpected compatible record: %#v", record)
	}
}

func TestPasswordServiceUsesFreshCacheWithoutHTTP(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 30, 0, 0, chinaLocation)
	record, err := validateDailyPasswordResponse(validDailyPasswordData(now), now)
	if err != nil {
		t.Fatal(err)
	}
	record.FetchedAt = now.Add(-10 * time.Minute).Format(time.RFC3339)
	actions := &fakeActions{kv: map[string]any{passwordCacheKey(record.Date): record}}
	service, err := newPasswordService(actions, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if actions.httpCalls != 0 || !got.Cached {
		t.Fatalf("fresh cache result = %#v, HTTP calls = %d", got, actions.httpCalls)
	}
}

func TestPasswordServiceUsesSameDayCacheWhenRefreshFails(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 30, 0, 0, chinaLocation)
	record, err := validateDailyPasswordResponse(validDailyPasswordData(now), now)
	if err != nil {
		t.Fatal(err)
	}
	record.FetchedAt = now.Add(-2 * time.Hour).Format(time.RFC3339)
	actions := &fakeActions{kv: map[string]any{passwordCacheKey(record.Date): record}, httpErr: errors.New("offline")}
	service, err := newPasswordService(actions, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if actions.httpCalls != 1 || !got.Cached {
		t.Fatalf("stale cache result = %#v, HTTP calls = %d", got, actions.httpCalls)
	}
}

func TestPasswordServiceFetchesDateURLAndCachesValidatedResponse(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 30, 0, 0, chinaLocation)
	payload, err := json.Marshal(validDailyPasswordData(now))
	if err != nil {
		t.Fatal(err)
	}
	actions := &fakeActions{httpResult: rayleabot.ActionResult{"status_code": 200, "body_text": string(payload)}, kv: map[string]any{}}
	service, err := newPasswordService(actions, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if record.Cached || actions.httpCalls != 1 {
		t.Fatalf("unexpected fetched record: %#v", record)
	}
	if actions.httpRequest.URL != "https://db.18183.com/sjzmm/data/daily/2026-09-03.json" {
		t.Fatalf("request URL = %q", actions.httpRequest.URL)
	}
	if _, exists := actions.kv[passwordCacheKey("2026-09-03")]; !exists {
		t.Fatal("validated response was not cached")
	}
}

func TestPasswordSettingsRejectInsecureURL(t *testing.T) {
	_, err := readPasswordSettings(map[string]any{"password_api_url": "http://example.com/passwords/{date}.json"})
	if err == nil {
		t.Fatal("readPasswordSettings() error = nil")
	}
}

func validDailyPasswordData(now time.Time) dailyPasswordEnvelope {
	return dailyPasswordEnvelope{
		Date:      now.In(chinaLocation).Format("2006-01-02"),
		UpdatedAt: now.In(chinaLocation).Format(time.RFC3339),
		Entries: map[string]string{
			"AZ3":  "2015",
			"零号大坝": "5772",
			"长弓溪谷": "5577",
			"巴克什":  "4524",
			"航天基地": "0136",
			"潮汐监狱": "4375",
		},
	}
}

func validTminiPasswordData(now time.Time) tminiData {
	entries := make([]tminiPassword, 0, len(expectedPasswordMaps))
	passwords := []string{"5772", "5577", "4524", "0136", "4375", "2015"}
	for index, mapName := range expectedPasswordMaps {
		entries = append(entries, tminiPassword{
			MapName:  mapName,
			Password: passwords[index],
			LocationInfo: tminiLocationInfo{
				Description: mapName + "密码门位置",
			},
		})
	}
	return tminiData{
		UpdateDate:  now.Format("01月02日") + "每日密码已更新",
		TotalCount:  len(entries),
		Passwords:   entries,
		Source:      "兼容源",
		LastUpdated: now.Format("2006-01-02 15:04:05"),
	}
}
