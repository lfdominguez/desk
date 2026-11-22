package infra

// deskTools are the desk Claude's, the one in Desk Control's General chat:
// the whole board, every organization's, through the same card tools'
// socket (its Owner.Card is "desk:"). It makes and messages cards; it
// never edits files or runs commands.
var deskTools = []map[string]any{
	{
		"name": "cards",
		"description": "The cards on desk's board: key, organization, column, status, title and ticket. " +
			"Look before making a card, so the same work isn't made twice.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"org":    map[string]any{"type": "string", "description": "Only this organization's."},
			"query":  map[string]any{"type": "string", "description": "Fuzzy words to match titles, notes and tickets."},
			"column": map[string]any{"type": "string", "description": "Only this column (Backlog, To do, In progress, Blocked, Review, Done)."}}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name":        "card_read",
		"description": "One card by its key (DESK-n): column, status and its note, notes, links and its last messages.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"key": map[string]any{"type": "string"}}, "required": []string{"key"}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "digest",
		"description": "What was done and what is in flight since a day, by organization, as a standup reads it.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"since": map[string]any{"type": "string", "enum": []string{"today", "yesterday", "week"}}}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "card_create",
		"description": "Make a card in an organization, when the user asks for one. It waits in To do unless start, " +
			"which starts its Claude on it at once. Returns its key.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"org":   map[string]any{"type": "string"},
			"title": map[string]any{"type": "string", "description": "What the card is to do, one line."},
			"notes": map[string]any{"type": "string", "description": "The brief: context, steps, what done means."},
			"start": map[string]any{"type": "boolean"}},
			"required": []string{"org", "title"}},
	},
	{
		"name":        "card_message",
		"description": "Send a message to a card's Claude as the user's own, when the user asks to tell a card something.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"key":  map[string]any{"type": "string"},
			"text": map[string]any{"type": "string"}}, "required": []string{"key", "text"}},
	},
}

// DeskMCP is claude's --mcp-config giving the desk Claude deskTools
// through sock (ServeCards'), and the tools to allow without asking.
func DeskMCP(sock string) (string, []string) { return toolsMCP(sock, "desk", deskTools) }
