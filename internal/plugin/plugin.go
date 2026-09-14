package plugin

import (
	"context"
	"fmt"
	"strings"
	"time"

	rayleabot "github.com/RayleaBot/RayleaBot/sdk/go"
)

type hostActions interface {
	HTTPRequest(context.Context, httpRequest) (rayleabot.ActionResult, error)
	KVGet(context.Context, string) (rayleabot.ActionResult, error)
	KVSet(context.Context, string, any) (rayleabot.ActionResult, error)
	RenderImage(context.Context, rayleabot.RenderImageRequest) (rayleabot.ActionResult, error)
	LoggerWrite(context.Context, rayleabot.LoggerWriteRequest) (rayleabot.ActionResult, error)
}

type application struct {
	catalog *lootCatalog
	loot    *lootEngine
	now     func() time.Time
}

var defaultApplication = mustApplication()

func mustApplication() *application {
	catalog, err := loadCatalog()
	if err != nil {
		panic(err)
	}
	return &application{catalog: catalog, loot: newLootEngine(catalog, cryptoRandom{}), now: time.Now}
}

// Run starts the RayleaBot plugin runtime.
func Run(ctx context.Context) error {
	return rayleabot.Run(ctx, rayleabot.Options{}, rayleabot.HandlerFunc(defaultApplication.handleEvent))
}

func (app *application) handleEvent(ctx context.Context, event *rayleabot.EventContext) error {
	request := parseCommand(event.Event)
	switch request.Kind {
	case requestLoot:
		if request.Query == "" {
			return app.sendContainerList(event)
		}
		return app.sendLoot(ctx, event, request.Query)
	case requestContainerList:
		return app.sendContainerList(event)
	case requestPassword:
		return app.sendPasswords(ctx, event)
	default:
		return event.Result(map[string]any{"handled": false})
	}
}

func (app *application) sendLoot(ctx context.Context, event *rayleabot.EventContext, query string) error {
	if _, exists := app.catalog.findContainer(query); !exists {
		suggestions := app.catalog.suggestions(query, 5)
		return event.SendText("暂不支持「" + strings.TrimSpace(query) + "」。可以试试：" + strings.Join(suggestions, "、") + "。发送「可摸容器」查看完整目录。")
	}
	result, err := app.loot.roll(query)
	if err != nil {
		app.log(ctx, runtimeActions{Actions: event.Actions()}, "error", "摸容器模拟失败。", map[string]any{"container": query, "error": err.Error()})
		return event.SendText("这次没有摸到结果，请稍后再试。")
	}
	now := app.now().In(chinaLocation)
	fallback := formatLoot(result)
	rendered, renderErr := event.Actions().RenderImage(ctx, rayleabot.RenderImageRequest{
		Template: "delta-loot",
		Data: lootRenderData(
			result,
			event.Event.Actor.ID,
			event.Event.Actor.Nickname,
			event.Event.EventID,
			now,
			app.catalog,
		),
		Output:       "png",
		FallbackText: fallback,
	})
	if renderErr != nil {
		app.log(ctx, runtimeActions{Actions: event.Actions()}, "warn", "摸容器图片生成失败，改用文字回复。", map[string]any{"container": result.Container.Name, "error": renderErr.Error()})
		return event.SendText(fallback)
	}
	imagePath, _ := rendered["image_path"].(string)
	if strings.TrimSpace(imagePath) == "" {
		app.log(ctx, runtimeActions{Actions: event.Actions()}, "warn", "未生成摸容器图片，改用文字回复。", map[string]any{"container": result.Container.Name})
		return event.SendText(fallback)
	}
	return event.Send(event.Event.Target.Type, event.Event.Target.ID, rayleabot.Image(imagePath))
}

func (app *application) sendContainerList(event *rayleabot.EventContext) error {
	lines := []string{"可摸容器（共 " + fmt.Sprint(len(app.catalog.containers)) + " 种）"}
	for _, group := range app.catalog.containerGroups() {
		lines = append(lines, group["label"].(string)+"："+strings.Join(group["names"].([]string), "、"))
	}
	lines = append(lines, "示例：/摸航空箱 或 /摸 航空箱", "结果使用娱乐模拟概率，不代表游戏实际掉落。")
	return event.SendText(strings.Join(lines, "\n"))
}

func (app *application) sendPasswords(ctx context.Context, event *rayleabot.EventContext) error {
	service, err := newPasswordService(runtimeActions{Actions: event.Actions()}, event.Config, app.now)
	if err != nil {
		app.log(ctx, runtimeActions{Actions: event.Actions()}, "error", "每日密码配置无效；请在插件设置中修正接口地址或超时。", map[string]any{"error": err.Error()})
		return event.SendText("每日密码配置无效，请联系管理员检查插件设置。")
	}
	record, err := service.get(ctx)
	if err != nil {
		app.log(ctx, runtimeActions{Actions: event.Actions()}, "warn", "今日密码获取失败，暂无当天的可用缓存。", map[string]any{"error": err.Error()})
		return event.SendText("今日密码暂时无法获取，且没有可验证的当天缓存。请稍后再试。")
	}
	fallback := formatPasswords(record)
	rendered, renderErr := event.Actions().RenderImage(ctx, rayleabot.RenderImageRequest{
		Template:     "delta-passwords",
		Data:         passwordRenderData(record),
		Output:       "png",
		FallbackText: fallback,
	})
	if renderErr != nil {
		app.log(ctx, runtimeActions{Actions: event.Actions()}, "warn", "每日密码图片生成失败，改用文字回复。", map[string]any{"date": record.Date, "error": renderErr.Error()})
		return event.SendText(fallback)
	}
	imagePath, _ := rendered["image_path"].(string)
	if strings.TrimSpace(imagePath) == "" {
		app.log(ctx, runtimeActions{Actions: event.Actions()}, "warn", "未生成每日密码图片，改用文字回复。", map[string]any{"date": record.Date})
		return event.SendText(fallback)
	}
	return event.Send(event.Event.Target.Type, event.Event.Target.ID, rayleabot.Image(imagePath))
}

func (app *application) log(ctx context.Context, actions hostActions, level, message string, fields map[string]any) {
	// Diagnostics are best-effort because logging must not replace the user-facing terminal response.
	_, _ = actions.LoggerWrite(ctx, rayleabot.LoggerWriteRequest{Level: level, Message: message, Fields: fields})
}
