# 三角洲助手

面向 RayleaBot 的《三角洲行动》独立插件，提供摸容器娱乐模拟和每日密码查询。插件使用 manifest v4、plugin protocol v4 与宿主图片渲染，不修改 RayleaBot 主仓库。

## 功能

### 摸容器

```text
/摸航空箱
/摸 航空箱
/可摸容器
```

插件内置 28 种常规容器、常用别名、容器专属物品类别和四档模拟权重。抽取结果包含物品名称、品质与类别，并由宿主生成战利品查验单图片。

概率用于娱乐模拟，不代表《三角洲行动》官方掉落概率。容器和物品数据版本分别显示在结果图底部。

### 每日密码

```text
/三角洲密码
/每日密码
```

插件按北京时间拼接当天的结构化 JSON 地址，检查数据日期、更新时间、六张地图的完整性、地图唯一性和四位数字格式。当天结果缓存 30 分钟；上游不可用时只允许使用当天已验证缓存，不会把昨日密码当作今日密码。自定义接口也可使用兼容的 Tmini JSON 结构，但同样必须通过完整校验。

## 配置

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `password_api_url` | `https://db.18183.com/sjzmm/data/daily/{date}.json` | 每日密码 HTTPS 地址；`{date}` 会替换为北京时间 `YYYY-MM-DD`，也兼容 Tmini JSON 结构 |
| `password_cache_minutes` | `30` | 当天缓存刷新间隔，限制为 5～720 分钟 |
| `password_timeout_seconds` | `8` | 单次请求超时，限制为 3～30 秒 |

## 目录

```text
cmd/delta-force/       插件进程入口
internal/assets/       编译进二进制的容器、物品和权重数据
internal/plugin/       指令、抽取、密码校验、缓存和渲染编排
templates/             战利品与每日密码渲染模板
docs/sources.md        数据依据、取舍和更新规则
```

## 开发与验证

插件依赖 RayleaBot Go SDK v0.7.0。主仓库开发工作区会生成临时 `go.work`，把本插件与当前 SDK 连接起来，不需要在 `go.mod` 写本地 `replace`。

在已配置 RayleaBot 开发工作区后运行：

```text
go test ./...
go vet ./...
raylea-plugin inspect --plugin .
raylea-plugin build-go --plugin . --backend ./cmd/delta-force --target windows-x64 --out dist
```

正式发布还应分别构建 `linux-x64` 与 `macos-arm64` artifact，并检查 ZIP 单根目录、目标平台和原生入口。

## 数据与素材

- 插件不包含腾讯或第三方游戏图片、Logo、字体和模板。
- 物品名称与容器类别是公开游戏资料中的事实性信息；编辑后的数据表和模拟权重随插件发布。
- 第三方密码接口是可替换的数据源，不构成可用性保证。
- 运行时错误只显示必要的恢复提示，不向群聊输出上游原始响应。

## 许可证

本项目使用 GNU Affero General Public License v3.0，详见 [LICENSE](./LICENSE)。
