package lsp

import (
	"encoding/json"
	"strings"
)

var effectKeywords = []CompletionItem{
	{
		Label:            "effect",
		Kind:             14, // Keyword
		Detail:           "Declare an algebraic effect type",
		InsertText:       "effect ${1:name} {\n\tctl ${2:op}(${3:params}) ${4:returnType}\n}",
		InsertTextFormat: 2, // Snippet
	},
	{
		Label:            "perform",
		Kind:             14,
		Detail:           "Perform an effect operation",
		InsertText:       "perform ${1:effect}.${2:op}(${3:args})",
		InsertTextFormat: 2,
	},
	{
		Label:            "try",
		Kind:             14,
		Detail:           "Try block with effect handler",
		InsertText:       "try {\n\t${1}\n} handle ${2:effect} {\n\tctl ${3:op}(${4:params}) {\n\t\tresume with ${5:value}\n\t}\n}",
		InsertTextFormat: 2,
	},
	{
		Label:            "handle",
		Kind:             14,
		Detail:           "Handle an effect",
		InsertText:       "handle ${1:effect} {\n\tctl ${2:op}(${3:params}) {\n\t\tresume with ${4:value}\n\t}\n}",
		InsertTextFormat: 2,
	},
	{
		Label:            "resume with",
		Kind:             14,
		Detail:           "Resume at perform site with a value",
		InsertText:       "resume with ${1:value}",
		InsertTextFormat: 2,
	},
	{
		Label:            "resume",
		Kind:             14,
		Detail:           "Resume at perform site (void)",
		InsertText:       "resume",
		InsertTextFormat: 1,
	},
	{
		Label:            "ctl",
		Kind:             14,
		Detail:           "Control operation (may or may not resume)",
		InsertText:       "ctl ${1:name}(${2:params}) ${3:returnType}",
		InsertTextFormat: 2,
	},
	{
		Label:            "test",
		Kind:             14,
		Detail:           "Test block",
		InsertText:       "test \"${1:description}\" {\n\ttry {\n\t\t${2}\n\t\tassert ${3:condition}\n\t} handle ${4:effect} {\n\t\tctl ${5:op}(${6:params}) {\n\t\t\tresume with ${7:value}\n\t\t}\n\t}\n}",
		InsertTextFormat: 2,
	},
	{
		Label:            "assert",
		Kind:             14,
		Detail:           "Assert a condition in a test",
		InsertText:       "assert ${1:condition}",
		InsertTextFormat: 2,
	},
	{
		Label:            "handler_set",
		Kind:             14,
		Detail:           "Define a handler set for an environment",
		InsertText:       "handler_set ${1:name} {\n\thandle ${2:effect} {\n\t\tctl ${3:op}(${4:params}) {\n\t\t\tresume with ${5:value}\n\t\t}\n\t}\n}",
		InsertTextFormat: 2,
	},
}

// effectCompletions returns completions for effect keywords matching the current prefix.
func effectCompletions(linePrefix string) []CompletionItem {
	trimmed := strings.TrimSpace(linePrefix)
	words := strings.Fields(trimmed)
	var lastWord string
	if len(words) > 0 {
		lastWord = strings.ToLower(words[len(words)-1])
	}

	var items []CompletionItem
	for _, kw := range effectKeywords {
		if lastWord == "" || strings.HasPrefix(strings.ToLower(kw.Label), lastWord) {
			items = append(items, kw)
		}
	}
	return items
}

// handleCompletion merges effect keyword completions with gopls completions.
func (s *Server) handleCompletion(req Request) (any, error) {
	var params CompletionParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return CompletionList{}, nil
	}

	// Get current line prefix for context-aware completions
	doc, ok := s.docs.Get(params.TextDocument.URI)
	var linePrefix string
	if ok {
		linePrefix = getLinePrefix(doc.Content, params.Position)
	}

	// Effect keyword completions
	items := effectCompletions(linePrefix)

	// Proxy to gopls for Go completions
	if s.gopls != nil && s.gopls.Running() {
		shadowParams := params
		shadowParams.TextDocument.URI = s.shadow.TgToShadow(params.TextDocument.URI)

		resp, err := s.gopls.Call("textDocument/completion", shadowParams)
		if err == nil {
			var goplsResp Response
			if json.Unmarshal(resp, &goplsResp) == nil && goplsResp.Result != nil {
				resultBytes, _ := json.Marshal(goplsResp.Result)
				var goplsList CompletionList
				if json.Unmarshal(resultBytes, &goplsList) == nil {
					items = append(items, goplsList.Items...)
				}
			}
		}
	}

	return CompletionList{
		IsIncomplete: false,
		Items:        items,
	}, nil
}

// getLinePrefix returns the text on the given line up to the cursor position.
func getLinePrefix(content string, pos Position) string {
	lines := strings.Split(content, "\n")
	if pos.Line >= len(lines) {
		return ""
	}
	line := lines[pos.Line]
	if pos.Character > len(line) {
		return line
	}
	return line[:pos.Character]
}
