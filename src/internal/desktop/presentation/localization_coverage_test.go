package presentation

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestVisibleSourceMessagesHaveCatalogEntries(t *testing.T) {
	data, err := os.ReadFile("../../../../l10n/en.json")
	if err != nil {
		t.Fatal(err)
	}
	var messages map[string]json.RawMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		t.Fatal(err)
	}
	require := func(value, location string) {
		if value == "MyGOFrame" || value == "‹" || value == "›" {
			return
		}
		if _, exists := messages[value]; !exists {
			t.Errorf("missing catalog message %q at %s", value, location)
		}
	}
	for _, category := range settingsCategories {
		require(category.title, "category title")
		require(category.description, "category description")
		require(settingsCardLabel(category.title), "category accessibility label")
	}
	for _, name := range []string{"app.go", "fluent_ui.go", "about.go", "presets.go", "recording.go", "presentation.go", "localization.go", "debug.go"} {
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			var argument ast.Expr
			if identifier, ok := call.Fun.(*ast.Ident); ok && len(call.Args) > 1 {
				switch identifier.Name {
				case "text", "message", "field", "helpText", "modalHeader":
					argument = call.Args[1]
				}
			}
			if selection, ok := call.Fun.(*ast.SelectorExpr); ok {
				if identifier, ok := selection.X.(*ast.Ident); ok && identifier.Name == "ui" && len(call.Args) > 1 {
					switch selection.Sel.Name {
					case "Text", "Button", "PrimaryButton", "Link":
						argument = call.Args[1]
					case "Checkbox":
						if len(call.Args) > 2 {
							argument = call.Args[2]
						}
					}
				} else if (selection.Sel.Name == "Label" || selection.Sel.Name == "Tooltip" || selection.Sel.Name == "Placeholder") && len(call.Args) > 0 {
					argument = call.Args[0]
				}
			}
			if literal, ok := argument.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(value) != "" {
					require(value, set.Position(literal.Pos()).String())
				}
			}
			return true
		})
	}
}
