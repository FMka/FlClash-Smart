package core

import (
	"errors"
	"strings"

	"github.com/metacubex/mihomo/config"
	"go.yaml.in/yaml/v3"
)

type smartSettings struct {
	Enabled         bool     `yaml:"enabled"`
	AllowRussian    bool     `yaml:"allow-russian"`
	Services        []string `yaml:"services"`
	MobileBudgetMiB int64    `yaml:"mobile-budget-mib"`
	WiFiBudgetMiB   int64    `yaml:"wifi-budget-mib"`
}

func defaultSmartSettings() smartSettings {
	return smartSettings{Enabled: true, Services: []string{"youtube", "google", "chatgpt", "telegram"}, MobileBudgetMiB: 20, WiFiBudgetMiB: 100}
}

func decodeSmartSettings(data []byte) (smartSettings, error) {
	settings := defaultSmartSettings()
	var root struct {
		Settings *yaml.Node `yaml:"smart-reconnect-settings"`
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return settings, err
	}
	mapping, err := documentMapping(&document)
	if err != nil {
		return settings, err
	}
	root.Settings = mappingValue(mapping, "smart-reconnect-settings")
	if root.Settings != nil {
		if err = root.Settings.Decode(&settings); err != nil {
			return settings, err
		}
	}
	if settings.MobileBudgetMiB < 0 || settings.WiFiBudgetMiB < 0 || settings.MobileBudgetMiB > 10240 || settings.WiFiBudgetMiB > 10240 {
		return settings, errors.New("smart speed budget must be between 0 and 10240 MiB")
	}
	known := map[string]bool{}
	for _, checker := range serviceCheckers {
		known[checker.name] = true
	}
	seen := map[string]bool{}
	services := []string{}
	for _, name := range settings.Services {
		if !known[name] {
			return settings, errors.New("unknown smart priority service")
		}
		if !seen[name] {
			services = append(services, name)
			seen[name] = true
		}
	}
	settings.Services = services
	return settings, nil
}

func smartStrings(value any) []string {
	switch values := value.(type) {
	case []string:
		return values
	case []any:
		list := []string{}
		for _, value := range values {
			if item, ok := value.(string); ok {
				list = append(list, item)
			}
		}
		return list
	}
	return nil
}

func primarySmartGroup(raw *config.RawConfig) string {
	groups := map[string]map[string]any{}
	for _, group := range raw.ProxyGroup {
		name, _ := group["name"].(string)
		groups[name] = group
	}
	eligible := func(name string) bool {
		group := groups[name]
		return group != nil && group["smart-reconnect"] != false && (group["type"] == "select" || group["type"] == "url-test")
	}
	for _, rule := range raw.Rule {
		parts := strings.Split(rule, ",")
		if len(parts) > 1 && strings.EqualFold(strings.TrimSpace(parts[0]), "MATCH") {
			name := strings.TrimSpace(parts[1])
			if eligible(name) {
				return name
			}
		}
	}
	for _, group := range raw.ProxyGroup {
		name, _ := group["name"].(string)
		if group["type"] == "url-test" && eligible(name) {
			return name
		}
	}
	for _, group := range raw.ProxyGroup {
		name, _ := group["name"].(string)
		if eligible(name) {
			return name
		}
	}
	return ""
}

// Flatten only the managed selector. The source groups and routing rules keep
// their own filters; dynamic provider members are resolved by the backend.
func flattenSmartGroup(raw *config.RawConfig, group map[string]any) {
	groups := map[string]map[string]any{}
	for _, entry := range raw.ProxyGroup {
		name, _ := entry["name"].(string)
		groups[name] = entry
	}
	seen := map[string]bool{}
	var leaves, uses []string
	var visit func(map[string]any)
	visit = func(entry map[string]any) {
		if entry["type"] == "relay" {
			return
		}
		name, _ := entry["name"].(string)
		if seen[name] {
			return
		}
		seen[name] = true
		for _, name := range smartStrings(entry["proxies"]) {
			if child := groups[name]; child != nil {
				visit(child)
			} else if name != "DIRECT" && name != "REJECT" && !strings.HasPrefix(name, "COMPATIBLE") {
				leaves = append(leaves, name)
			}
		}
		uses = append(uses, smartStrings(entry["use"])...)
	}
	visit(group)
	group["proxies"] = append([]string{"REJECT"}, uniqueSmartStrings(leaves)...)
	if len(uses) > 0 {
		group["use"] = uniqueSmartStrings(uses)
	}
}

func uniqueSmartStrings(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
