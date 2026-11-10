package localization

import (
	"encoding/json"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/egoist/mygo/i18n"
)

func TestCatalogMessagesAndArguments(t *testing.T) {
	var source map[string]json.RawMessage
	data, err := fs.ReadFile(files, "en.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	for _, language := range Languages() {
		t.Run(language.Tag, func(t *testing.T) {
			if language.Name == "" || i18n.Normalize(language.Tag) != language.Tag {
				t.Fatal("invalid language metadata")
			}
			data, err := fs.ReadFile(files, language.Tag+".json")
			if err != nil {
				t.Fatal(err)
			}
			var translations map[string]json.RawMessage
			if err := json.Unmarshal(data, &translations); err != nil {
				t.Fatal(err)
			}
			for key, original := range source {
				if strings.HasPrefix(key, "@") {
					continue
				}
				var originalText, translatedText string
				if err := json.Unmarshal(original, &originalText); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(translations[key], &translatedText); err != nil {
					t.Fatalf("%s: %v", key, err)
				}
				parsedSource, err := i18n.ParseMessage(originalText)
				if err != nil {
					t.Fatalf("source %s: %v", key, err)
				}
				parsedTranslation, err := i18n.ParseMessage(translatedText)
				if err != nil {
					t.Fatalf("%s: %v", key, err)
				}
				names := func(message *i18n.Message) []string {
					var result []string
					for _, argument := range message.Args() {
						result = append(result, argument.Name)
					}
					sort.Strings(result)
					return result
				}
				if !reflect.DeepEqual(names(parsedSource), names(parsedTranslation)) {
					t.Errorf("%s has different arguments: %v versus %v", key, names(parsedSource), names(parsedTranslation))
				}
				_, resolved, exists := catalog.Message(language.Tag, key)
				if !exists || resolved != language.Tag {
					t.Errorf("%s silently fell back to %s", key, resolved)
				}
			}
		})
	}
}

func TestLocaleMatchingFallbackAndInterpolation(t *testing.T) {
	for _, sample := range []struct{ locale, expected string }{
		{"ko-KR", "설정"}, {"ja-JP", "設定"}, {"en-GB", "Settings"}, {"fr-FR", "Settings"}, {"", "Settings"},
	} {
		if got := Text(sample.locale, "Settings"); got != sample.expected {
			t.Errorf("%s: %q", sample.locale, got)
		}
	}
	if value := Text("ja", "/home/사용자/録画.mkv"); value != "/home/사용자/録画.mkv" {
		t.Fatal("unknown metadata changed")
	}
	if value := Format("ko", "License for {name}", map[string]any{"name": "Library {name} <script>"}); value != "Library {name} <script> 라이선스" {
		t.Fatalf("argument was interpreted as a pattern: %q", value)
	}
	partial, err := i18n.NewCatalog(fstest.MapFS{
		"en.json": {Data: []byte(`{"Settings":"Settings","Close":"Close"}`)},
		"ko.json": {Data: []byte(`{"Settings":"설정"}`)},
	}, "en")
	if err != nil {
		t.Fatal(err)
	}
	if partial.Format("ko", "Close", nil) != "Close" {
		t.Fatal("missing translation did not use English")
	}
}

func TestStaticTranslationsAllocateNothing(t *testing.T) {
	for _, locale := range []string{"en", "ko", "ja"} {
		Text(locale, "Settings")
		if allocations := testing.AllocsPerRun(100, func() { Text(locale, "Settings") }); allocations != 0 {
			t.Errorf("%s: %g allocations", locale, allocations)
		}
	}
	if allocations := testing.AllocsPerRun(100, func() { Text("ko", "untranslated driver detail") }); allocations != 0 {
		t.Fatal("driver details allocated")
	}
}

func BenchmarkStaticTranslation(b *testing.B) {
	Text("ko", "Settings")
	b.ReportAllocs()
	for b.Loop() {
		Text("ko", "Settings")
	}
}
