package infra

// BoardTools are the board tools' names; the first two only read, and a
// card's workspace Claude and an autopilot's Claude get those.
var BoardTools = []string{"cards", "card_read", "card_create", "ticket_create"}

// boardToolDefs let a card's Claude see its organization's board and add
// to it in one call that answers, where the DESK-CARD and DESK-TICKET lines
// it replaces could fail unseen.
var boardToolDefs = []map[string]any{
	{
		"name": "cards",
		"description": "The cards on the board of this card's organization: key, column, status, title and ticket. " +
			"Look before making a card, so the same work isn't made twice.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"query":  map[string]any{"type": "string", "description": "Fuzzy words to match titles, notes and tickets."},
			"column": map[string]any{"type": "string", "description": "Only this column (Backlog, To do, In progress, Blocked, Review, Done)."}}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name":        "card_read",
		"description": "One card of this organization by its key (DESK-n): column, status and its note, notes, links and its last messages.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"key": map[string]any{"type": "string"}}, "required": []string{"key"}},
		"annotations": map[string]any{"readOnlyHint": true},
	},
	{
		"name": "card_create",
		"description": "Make a new card in this card's organization and folder, for work that isn't this card's (ask the user first). " +
			"It waits in its column until the user starts it; with ticket (the default) it creates its own ticket when it starts. Returns its key.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"title":  map[string]any{"type": "string", "description": "What the card is to do, one line."},
			"notes":  map[string]any{"type": "string", "description": "The brief: context, steps, what done means."},
			"column": map[string]any{"type": "string", "description": "Default To do."},
			"ticket": map[string]any{"type": "boolean", "description": "Default true."}},
			"required": []string{"title"}},
	},
	{
		"name": "ticket_create",
		"description": "Create this card's ticket in the organization's tracker (Jira, Linear or GitHub), assigned to the user, and link it " +
			"to the card. Only when the user asks and the card has none: a card tracks one ticket. Returns its key and URL, or why not.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"title":       map[string]any{"type": "string"},
			"description": map[string]any{"type": "string", "description": "Markdown: what is to be done and why."}},
			"required": []string{"title", "description"}},
	},
}

func init() {
	cardTools = append(cardTools, boardToolDefs...)
	workspaceTools = append(workspaceTools, boardToolDefs[:2]...)
	pilotTools = append(pilotTools, boardToolDefs[:2]...)
}
