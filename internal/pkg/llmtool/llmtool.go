// Package llmtool berisi bentuk tool yang dikirim ke model (format function
// calling OpenAI) dan hasil pemanggilannya. Dipakai bersama oleh sumber tool
// (MCP, pembuat gambar) dan proxy yang menjalankan putaran panggil-tool.
package llmtool

import "encoding/json"

// Spec adalah satu tool yang ditawarkan ke model.
type Spec struct {
	Name       string          // nama function yang dilihat model
	Source     string          // asal tool (nama server MCP, "image", ...), untuk log
	Definition json.RawMessage // {"type":"function","function":{...}}
}

// Result adalah hasil pemanggilan tool, berupa teks untuk model. Kegagalan
// juga dilaporkan sebagai teks (IsError) supaya model bisa menjelaskannya.
type Result struct {
	Text    string
	IsError bool
}

// Function menyusun definisi tool berformat OpenAI.
func Function(name, description string, parameters any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": description,
			"parameters":  parameters,
		},
	})
	return b
}
