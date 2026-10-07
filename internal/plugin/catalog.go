package plugin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/RayleaBot/delta-force/internal/assets"
)

const weightScale = 10_000

var rarityOrder = []string{"white", "green", "blue", "purple", "gold", "red"}

var rarityLabels = map[string]string{
	"white":  "白色",
	"green":  "绿色",
	"blue":   "蓝色",
	"purple": "紫色",
	"gold":   "金色",
	"red":    "红色",
}

var categoryLabels = map[string]string{
	"craft":       "工艺藏品",
	"electronics": "电子物品",
	"intel":       "资料情报",
	"energy":      "能源燃料",
	"medical":     "医疗道具",
	"tools":       "工具材料",
	"household":   "家居物品",
	"weapon":      "武器",
	"ammo":        "弹药",
	"equipment":   "装备",
	"keycard":     "门卡",
}

type containerDocument struct {
	SchemaVersion int         `json:"schema_version"`
	SourceVersion string      `json:"source_version"`
	Containers    []container `json:"containers"`
}

type itemDocument struct {
	SchemaVersion int    `json:"schema_version"`
	SourceVersion string `json:"source_version"`
	Items         []item `json:"items"`
}

type profileDocument struct {
	SchemaVersion int       `json:"schema_version"`
	Profiles      []profile `json:"profiles"`
}

type container struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases"`
	Profile    string   `json:"profile"`
	MinItems   int      `json:"min_items"`
	MaxItems   int      `json:"max_items"`
	Categories []string `json:"categories"`
}

type item struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Rarity   string `json:"rarity"`
	Category string `json:"category"`
}

type profile struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	Weights map[string]int `json:"weights"`
}

type lootCatalog struct {
	containers            []container
	items                 []item
	profiles              map[string]profile
	containerByNormalized map[string]int
	containerSource       string
	itemSource            string
}

func loadCatalog() (*lootCatalog, error) {
	var containers containerDocument
	if err := json.Unmarshal(assets.Containers, &containers); err != nil {
		return nil, fmt.Errorf("decode container catalog: %w", err)
	}
	var items itemDocument
	if err := json.Unmarshal(assets.Items, &items); err != nil {
		return nil, fmt.Errorf("decode item catalog: %w", err)
	}
	var profiles profileDocument
	if err := json.Unmarshal(assets.Profiles, &profiles); err != nil {
		return nil, fmt.Errorf("decode loot profiles: %w", err)
	}
	if containers.SchemaVersion != 1 || items.SchemaVersion != 1 || profiles.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported embedded catalog schema version")
	}

	catalog := &lootCatalog{
		containers:            append([]container(nil), containers.Containers...),
		items:                 append([]item(nil), items.Items...),
		profiles:              make(map[string]profile, len(profiles.Profiles)),
		containerByNormalized: make(map[string]int),
		containerSource:       containers.SourceVersion,
		itemSource:            items.SourceVersion,
	}
	for _, candidate := range profiles.Profiles {
		if _, exists := catalog.profiles[candidate.ID]; exists {
			return nil, fmt.Errorf("duplicate loot profile %q", candidate.ID)
		}
		total := 0
		for _, rarity := range rarityOrder {
			weight := candidate.Weights[rarity]
			if weight < 0 {
				return nil, fmt.Errorf("loot profile %q has negative %s weight", candidate.ID, rarity)
			}
			total += weight
		}
		if total != weightScale {
			return nil, fmt.Errorf("loot profile %q weights sum to %d, want %d", candidate.ID, total, weightScale)
		}
		catalog.profiles[candidate.ID] = candidate
	}
	if err := catalog.validateItems(); err != nil {
		return nil, err
	}
	if err := catalog.validateContainers(); err != nil {
		return nil, err
	}
	return catalog, nil
}

func (catalog *lootCatalog) validateItems() error {
	seenIDs := make(map[string]struct{}, len(catalog.items))
	seenNames := make(map[string]struct{}, len(catalog.items))
	for index, candidate := range catalog.items {
		if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.Name) == "" {
			return fmt.Errorf("items[%d] requires id and name", index)
		}
		if _, exists := seenIDs[candidate.ID]; exists {
			return fmt.Errorf("duplicate item id %q", candidate.ID)
		}
		seenIDs[candidate.ID] = struct{}{}
		if _, exists := seenNames[candidate.Name]; exists {
			return fmt.Errorf("duplicate item name %q", candidate.Name)
		}
		seenNames[candidate.Name] = struct{}{}
		if _, exists := rarityLabels[candidate.Rarity]; !exists {
			return fmt.Errorf("item %q has unknown rarity %q", candidate.ID, candidate.Rarity)
		}
		if _, exists := categoryLabels[candidate.Category]; !exists {
			return fmt.Errorf("item %q has unknown category %q", candidate.ID, candidate.Category)
		}
	}
	return nil
}

func (catalog *lootCatalog) validateContainers() error {
	seenIDs := make(map[string]struct{}, len(catalog.containers))
	for index, candidate := range catalog.containers {
		if strings.TrimSpace(candidate.ID) == "" || strings.TrimSpace(candidate.Name) == "" {
			return fmt.Errorf("containers[%d] requires id and name", index)
		}
		if _, exists := seenIDs[candidate.ID]; exists {
			return fmt.Errorf("duplicate container id %q", candidate.ID)
		}
		seenIDs[candidate.ID] = struct{}{}
		if candidate.MinItems < 1 || candidate.MaxItems < candidate.MinItems || candidate.MaxItems > 8 {
			return fmt.Errorf("container %q has invalid item range %d..%d", candidate.ID, candidate.MinItems, candidate.MaxItems)
		}
		lootProfile, exists := catalog.profiles[candidate.Profile]
		if !exists {
			return fmt.Errorf("container %q references unknown profile %q", candidate.ID, candidate.Profile)
		}
		if len(candidate.Categories) == 0 {
			return fmt.Errorf("container %q has no item categories", candidate.ID)
		}
		for _, category := range candidate.Categories {
			if _, exists := categoryLabels[category]; !exists {
				return fmt.Errorf("container %q has unknown category %q", candidate.ID, category)
			}
		}
		for rarity, weight := range lootProfile.Weights {
			if weight > 0 && len(catalog.candidates(candidate, rarity, nil)) == 0 {
				return fmt.Errorf("container %q has no %s candidates", candidate.ID, rarity)
			}
		}
		for _, alias := range append([]string{candidate.Name}, candidate.Aliases...) {
			key := normalizeContainer(alias)
			if key == "" {
				return fmt.Errorf("container %q has an empty alias", candidate.ID)
			}
			if other, exists := catalog.containerByNormalized[key]; exists {
				return fmt.Errorf("container alias %q conflicts between %q and %q", alias, catalog.containers[other].ID, candidate.ID)
			}
			catalog.containerByNormalized[key] = index
		}
	}
	return nil
}

func (catalog *lootCatalog) findContainer(query string) (container, bool) {
	index, exists := catalog.containerByNormalized[normalizeContainer(query)]
	if !exists {
		return container{}, false
	}
	return catalog.containers[index], true
}

func (catalog *lootCatalog) candidates(owner container, rarity string, excluded map[string]struct{}) []item {
	allowed := make(map[string]struct{}, len(owner.Categories))
	for _, category := range owner.Categories {
		allowed[category] = struct{}{}
	}
	result := make([]item, 0)
	for _, candidate := range catalog.items {
		if candidate.Rarity != rarity {
			continue
		}
		if _, exists := allowed[candidate.Category]; !exists {
			continue
		}
		if _, exists := excluded[candidate.ID]; exists {
			continue
		}
		result = append(result, candidate)
	}
	return result
}

func (catalog *lootCatalog) containerNames() []string {
	names := make([]string, 0, len(catalog.containers))
	for _, candidate := range catalog.containers {
		names = append(names, candidate.Name)
	}
	return names
}

func (catalog *lootCatalog) containerGroups() []map[string]any {
	profileOrder := []string{"s", "a", "b", "c"}
	groups := make([]map[string]any, 0, len(profileOrder))
	for _, profileID := range profileOrder {
		lootProfile := catalog.profiles[profileID]
		names := make([]string, 0)
		for _, candidate := range catalog.containers {
			if candidate.Profile == profileID {
				names = append(names, candidate.Name)
			}
		}
		groups = append(groups, map[string]any{"id": profileID, "label": lootProfile.Label, "names": names})
	}
	return groups
}

func (catalog *lootCatalog) suggestions(query string, limit int) []string {
	key := normalizeContainer(query)
	matched := make([]string, 0, limit)
	for _, candidate := range catalog.containers {
		if strings.Contains(normalizeContainer(candidate.Name), key) || strings.Contains(key, normalizeContainer(candidate.Name)) {
			matched = append(matched, candidate.Name)
		}
	}
	if len(matched) == 0 {
		for _, candidate := range catalog.containers {
			if candidate.Profile == "s" {
				matched = append(matched, candidate.Name)
			}
		}
	}
	sort.Strings(matched)
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched
}

func normalizeContainer(value string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '-' || r == '_' || r == '·' {
			return -1
		}
		return r
	}, strings.TrimSpace(value)))
}
