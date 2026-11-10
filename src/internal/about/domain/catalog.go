package domain

import "strings"

type Document struct {
	Title  string `json:"title"`
	Source string `json:"source"`
	SHA256 string `json:"sha256"`
	Text   string `json:"text"`
}

type Component struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Scope     string   `json:"scope"`
	License   string   `json:"license"`
	URL       string   `json:"url"`
	Documents []string `json:"documents"`
}

type Catalog struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Version       string              `json:"version"`
	Revision      string              `json:"revision"`
	Modified      bool                `json:"modified"`
	Developer     string              `json:"developer"`
	Contact       string              `json:"contact"`
	Organization  string              `json:"organization"`
	ProjectURL    string              `json:"projectURL"`
	Components    []Component         `json:"components"`
	Documents     map[string]Document `json:"documents"`
}

func (catalog *Catalog) LicenseText(component Component) string {
	var result strings.Builder
	for _, identifier := range component.Documents {
		document, exists := catalog.Documents[identifier]
		if !exists {
			continue
		}
		if result.Len() != 0 {
			result.WriteString("\n\n")
		}
		result.WriteString(document.Title + "\nSource: " + document.Source + "\n\n")
		result.WriteString(document.Text)
	}
	return result.String()
}
