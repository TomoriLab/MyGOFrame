// Package localization provides the native desktop's embedded translation catalogs.
package localization

import (
	"embed"
	"encoding/json"
	"io/fs"

	"github.com/egoist/mygo/i18n"
	// MyGo's native menus and dialogs also require the SDK's complete Japanese catalog.
	_ "github.com/egoist/mygo/locales/ja"
)

// The build wrapper copies the authoring catalogs here so binaries need no data files.
//
//go:embed catalogs_generated/*.json
var resources embed.FS

var files, catalog = load()
var sourceMessages = readSource()

func readSource() map[string]json.RawMessage {
	data, err := fs.ReadFile(files, "en.json")
	if err != nil {
		panic(err)
	}
	var messages map[string]json.RawMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		panic(err)
	}
	return messages
}

func load() (fs.FS, *i18n.Catalog) {
	files, err := fs.Sub(resources, "catalogs_generated")
	if err != nil {
		panic(err)
	}
	catalog, err := i18n.NewCatalog(files, "en")
	if err != nil {
		panic(err)
	}
	i18n.Register(catalog)
	return files, catalog
}

// Text uses English source messages as keys, keeping untranslated diagnostics intact.
func Text(locale, message string) string {
	// Device metadata and changing diagnostic details must not populate the message cache.
	if _, exists := sourceMessages[message]; !exists {
		return message
	}
	return catalog.Format(locale, message, nil)
}

// Format uses named ICU arguments so translations may change their order.
func Format(locale, message string, arguments map[string]any) string {
	return catalog.Format(locale, message, arguments)
}

// Languages returns each catalog's language name in its own language.
func Languages() []Language {
	languages := make([]Language, 0, len(catalog.Languages()))
	for _, tag := range catalog.Languages() {
		data, err := fs.ReadFile(files, tag+".json")
		if err != nil {
			panic(err)
		}
		var metadata struct {
			Name string `json:"@@name"`
		}
		if err := json.Unmarshal(data, &metadata); err != nil {
			panic(err)
		}
		languages = append(languages, Language{Tag: tag, Name: metadata.Name})
	}
	return languages
}

type Language struct{ Tag, Name string }
