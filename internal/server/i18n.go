package server

import (
	"encoding/json"
	"io/fs"
	"strings"
)

// Backend messages share the frontend's Chinese baseline. Keep fmt verbs intact.
var uiCatalog = func() map[string]any {
	data, err := fs.ReadFile(embeddedAppAssets, "static/strings/zh-CN.json")
	if err != nil {
		panic(err)
	}
	var catalog map[string]any
	if err := json.Unmarshal(data, &catalog); err != nil {
		panic(err)
	}
	return catalog
}()

func uiText(key string) string {
	var value any = uiCatalog
	for _, part := range strings.Split(key, ".") {
		group, ok := value.(map[string]any)
		if !ok {
			panic("invalid UI message key: " + key)
		}
		value = group[part]
	}
	text, ok := value.(string)
	if !ok {
		panic("missing UI message key: " + key)
	}
	return text
}
