package ui

import (
	"fmt"
	"image/color"
	"maps"
	"slices"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/lfdominguez/desk/internal/company"
	"github.com/lfdominguez/desk/internal/orgs"
	"github.com/lfdominguez/desk/internal/ui/kit"
)

// tierInfo says what each Claude model a provider stands in for is for,
// and the models that come close to it, as OpenRouter names them.
var tierInfo = map[string]struct {
	what string
	near []nearModel
}{
	"opus": {"The deepest thinker: plans, architecture, reviews and hard bugs. Slow and costly; the tech lead, architect, security, CTO and CISO roles run on it.", []nearModel{
		{"anthropic/claude-opus-5.5", "Claude Opus 5.5", "the real one, paid per token"},
		{"openai/gpt-6.1-sol", "GPT-6.1 Sol", "OpenAI's strongest"},
		{"google/gemini-4-carbon", "Gemini 4 Carbon", "Google's strongest"},
		{"moonshotai/kimi-k3.1-pro", "Kimi K3.1 Pro", "Moonshot's strongest, made for agents"},
		{"z-ai/glm-5.3-prime", "GLM 5.3 Prime", "Z.ai's strongest, cheaper"},
		{"qwen/qwen4-max", "Qwen4 Max", "Alibaba's strongest, cheaper"},
	}},
	"sonnet": {"The everyday workhorse: writes and edits code, runs tools, keeps a long task going. The senior developer, QA, DevOps and CFO roles run on it.", []nearModel{
		{"anthropic/claude-sonnet-5.5", "Claude Sonnet 5.5", "the real one, paid per token"},
		{"openai/gpt-6.1-terra", "GPT-6.1 Terra", "OpenAI's balanced one"},
		{"google/gemini-4-hydrogen", "Gemini 4 Hydrogen", "Google's balanced one"},
		{"z-ai/glm-5.3", "GLM 5.3", "a strong coder, very cheap"},
		{"deepseek/deepseek-v4-pro-0813", "DeepSeek V4 Pro", "a strong coder, cheap"},
		{"x-ai/grok-4.7", "Grok 4.7", "xAI's"},
	}},
	"haiku": {"A simple but intelligent model for simple tasks, fast and cheap: Improve with Claude, Explain, and Claude Code's own small chores (titles, summaries).", []nearModel{
		{"anthropic/claude-haiku-5.5", "Claude Haiku 5.5", "the real one, paid per token"},
		{"openai/gpt-6-luna", "GPT-6 Luna", "OpenAI's small one"},
		{"google/gemini-3.8-flash", "Gemini 3.8 Flash", "Google's fast one"},
		{"deepseek/deepseek-v4.1-flash", "DeepSeek V4.1 Flash", "very cheap"},
		{"z-ai/glm-5.3-flash", "GLM 5.3 Flash", "very cheap"},
		{"qwen/qwen3.8-flash", "Qwen3.8 Flash", "very cheap"},
	}},
}

// nearModel is a model that comes close to a Claude one: its id, as the
// provider takes it, a name to pick it by, and a word on it.
type nearModel struct{ id, name, note string }

// notClaude is what a picked model that isn't Claude may get wrong.
const notClaude = "Not Claude: desk's prompts are written for Claude, so it may now and then skip a status line or misuse a tool."

// customWarn is what a model desk doesn't know may get wrong.
const customWarn = "A model desk doesn't know. desk's prompts, tools and status lines are made for Claude: another model may not call tools, " +
	"skip the DESK-STATUS lines (cards don't move or say what they need), loop, or answer in the wrong shape (Improve with Claude and Explain fail). " +
	"Pick one that supports tool calling, and expect worse work than Claude's."

// taskInfo names each desk Claude [models] picks for, and says what it does.
var taskInfo = map[string][2]string{
	"card":      {"Card sessions", "The Claude that works a card: its conversation and its terminal. The one that writes code."},
	"improve":   {"Improve with Claude", "Rewrites a new card's request into a title and a prompt (Haiku)."},
	"explain":   {"Explain", "Breaks down a shell command Claude wants to run (Haiku)."},
	"workspace": {"Workspace", "The read-only Claude beside a card's files."},
	"lead":      {"Department leads", "The engineering, security and finance leads cards ask."},
	"drawing":   {"Drawings", "A drawing's own Claude, beside the canvas."},
	"infra":     {"Infra", "The infra Claude in the servers' shared terminals."},
	"workflow":  {"Learn workflow", "Reads how the organization's tickets move, for its Ticket workflow (Haiku)."},
}

// roleModels are the choices of a role's model, and what each writes ("" none).
var roleModels = [][2]string{{"", "Its own"}, {"opus", "Opus"}, {"sonnet", "Sonnet"}, {"haiku", "Haiku"}, {"inherit", "The session's"}}

// loadModels is an org page's Models part as read: [models]' provider per
// task and [company.models]' model per role, an editor each.
func loadModels(raw map[string]any) *cfgEntry {
	e := &cfgEntry{kind: "models", eds: map[string]*widget.Editor{}, src: map[string]int{}, on: map[string]bool{}}
	tasks, _ := raw["models"].(map[string]any)
	for _, t := range orgs.Tasks {
		s, _ := tasks[t].(string)
		e.ed("task." + t).SetText(s)
	}
	comp, _ := raw["company"].(map[string]any)
	roles, _ := comp["models"].(map[string]any)
	for _, r := range company.Roles() {
		role := strings.TrimPrefix(r.Name, company.Prefix)
		s, _ := roles[role].(string)
		e.ed("role." + role).SetText(s)
	}
	e.old, e.base = true, e.snap()
	return e
}

// providerNames is the model providers the page has, named.
func (d *cfgDialog) providerNames() []string {
	var out []string
	for _, e := range d.entries {
		if n := e.text("name"); e.kind == "provider" && n != "" {
			out = append(out, n)
		}
	}
	return out
}

// saveModels checks the providers and writes the Models part into out:
// a provider needs a name and its endpoint, two can't share a name, and a
// task can't run on one that isn't there. Keys desk doesn't know are kept.
func (d *cfgDialog) saveModels(out map[string]any) bool {
	names := d.providerNames()
	for _, e := range d.entries {
		switch {
		case e.kind != "provider":
		case e.text("name") == "":
			d.err, d.errAt = "A model provider needs a name", "models"
			return false
		case e.text("base_url") == "":
			d.err, d.errAt = e.text("name")+" needs its base URL", "models"
			return false
		}
	}
	for i, n := range names {
		if slices.Contains(names[:i], n) {
			d.err, d.errAt = "Two model providers are called "+n, "models"
			return false
		}
	}
	tasks, _ := out["models"].(map[string]any)
	tasks = maps.Clone(tasks)
	if tasks == nil {
		tasks = map[string]any{}
	}
	for _, t := range orgs.Tasks {
		v := d.models.text("task." + t)
		if v != "" && !slices.Contains(names, v) {
			d.err, d.errAt = fmt.Sprintf("%s run on %s, which isn't a model provider here", taskInfo[t][0], v), "models"
			return false
		}
		set(tasks, t, v)
	}
	set(out, "models", tasks)
	comp, _ := out["company"].(map[string]any)
	comp = maps.Clone(comp)
	if comp == nil {
		comp = map[string]any{}
	}
	roles, _ := comp["models"].(map[string]any)
	roles = maps.Clone(roles)
	if roles == nil {
		roles = map[string]any{}
	}
	for _, r := range company.Roles() {
		role := strings.TrimPrefix(r.Name, company.Prefix)
		set(roles, role, d.models.text("role."+role))
	}
	set(comp, "models", roles)
	set(out, "company", comp)
	return true
}

// set puts v in m at k, or takes k out when v is empty ("" or an empty table).
func set[V string | map[string]any](m map[string]any, k string, v V) {
	if len(v) == 0 {
		delete(m, k)
		return
	}
	m[k] = v
}

// cfgTier is a provider's model for one of Claude's (f.key: opus, sonnet,
// haiku): what that one is for, the models that come close, Not replaced,
// or Custom with what a model desk doesn't know may get wrong.
func (u *ui) cfgTier(gtx layout.Context, id string, e *cfgEntry, f cfgField) layout.Dimensions {
	k := u.k
	info := tierInfo[f.key]
	ed := e.ed(f.key)
	cur := slices.IndexFunc(info.near, func(m nearModel) bool { return m.id == strings.TrimSpace(ed.Text()) }) + 1
	if e.custom[f.key] || cur == 0 && strings.TrimSpace(ed.Text()) != "" {
		cur = len(info.near) + 1
	}
	names := []string{"Not replaced"}
	for _, m := range info.near {
		names = append(names, m.name)
	}
	names = append(names, "Custom")
	kids := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return k.Label(gtx, info.what, 12, kit.CText2, kit.Sans, 0)
		}),
		layout.Rigid(kit.VGap(6)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.choice(gtx, id+f.key, names, cur, func(int) color.NRGBA { return color.NRGBA{} }, func(i int) {
				if e.custom == nil {
					e.custom = map[string]bool{}
				}
				e.custom[f.key] = i == len(names)-1
				switch {
				case i == 0:
					ed.SetText("")
				case i < len(names)-1:
					ed.SetText(info.near[i-1].id)
				}
			})
		}),
	}
	note, tone := "", kit.CText3
	switch {
	case cur == 0:
		note = "Claude Code asks the provider for Claude's own " + f.label + " by its name."
	case cur <= len(info.near):
		m := info.near[cur-1]
		note = m.id + " · " + m.note
		if !strings.HasPrefix(m.id, "anthropic/") {
			note += ". " + notClaude
		}
		if !strings.Contains(e.text("base_url"), "openrouter.ai") {
			note += " (OpenRouter's name for it: another endpoint may call it otherwise.)"
		}
	default:
		kids = append(kids, layout.Rigid(kit.VGap(6)), layout.Rigid(k.Field(ed, "the model's id, as the provider names it: vendor/model", 0)))
		note, tone = customWarn, kit.COrange
	}
	kids = append(kids, layout.Rigid(kit.VGap(6)), layout.Rigid(func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, note, 11, tone, kit.Sans, 0) }))
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// cfgModels is an org page's Models part, under its servers: which desk
// Claude runs on the Claude subscription and which on a model provider, and
// the model each of desk's roles runs on.
func (u *ui) cfgModels(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	provs := d.providerNames()
	row := func(title, what string, choice layout.Widget) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(k.Lbl(title, 13, kit.CText, kit.Semi(kit.Sans))),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, what, 11, kit.CText3, kit.Sans, 0) }),
					layout.Rigid(kit.VGap(5)),
					layout.Rigid(choice))
			})
		})
	}
	none := func(int) color.NRGBA { return color.NRGBA{} }
	hint := "Each Claude desk starts runs on your Claude subscription, or on a model provider added above (an Anthropic-compatible endpoint: OpenRouter, a proxy). Each can differ: Explain on a provider, the card's work on the subscription."
	if len(provs) == 0 {
		hint = "Add a model provider above (an Anthropic-compatible endpoint: OpenRouter, a proxy) to run some of desk's Claudes on it instead of your Claude subscription."
	}
	kids := []layout.FlexChild{
		layout.Rigid(kit.VGap(18)), layout.Rigid(k.Caption("Models")), layout.Rigid(kit.VGap(4)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, hint, 12, kit.CText3, kit.Sans, 0) }),
		layout.Rigid(kit.VGap(12)),
	}
	for _, t := range orgs.Tasks {
		ed := d.models.ed("task." + t)
		names := append([]string{"Claude subscription"}, provs...)
		cur := max(0, slices.Index(names, ed.Text()))
		what := taskInfo[t][1]
		if v := ed.Text(); v != "" && !slices.Contains(provs, v) {
			cur, what = -1, what+" Its provider "+v+" isn't here any more: pick another."
		}
		kids = append(kids, row(taskInfo[t][0], what, func(gtx layout.Context) layout.Dimensions {
			return u.choice(gtx, "cfgTask"+t, names, cur, none, func(i int) {
				ed.SetText("")
				if i > 0 {
					ed.SetText(names[i])
				}
			})
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

// cfgRoles is the Company page's roles part: the model each of desk's
// roles runs on.
func (u *ui) cfgRoles(gtx layout.Context) layout.Dimensions {
	k, d := u.k, u.cfg
	none := func(int) color.NRGBA { return color.NRGBA{} }
	row := func(title, what string, choice layout.Widget) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Bottom: 12}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(k.Lbl(title, 13, kit.CText, kit.Semi(kit.Sans))),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return k.Label(gtx, what, 11, kit.CText3, kit.Sans, 0) }),
					layout.Rigid(kit.VGap(5)),
					layout.Rigid(choice))
			})
		})
	}
	var kids []layout.FlexChild
	kids = append(kids, layout.Rigid(kit.VGap(18)), layout.Rigid(k.Caption("Company roles")), layout.Rigid(kit.VGap(4)),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return k.Label(gtx, "The model each of desk's roles runs on, in the card session's subagents. On a provider, Opus, Sonnet and Haiku are what it puts in their place.", 12, kit.CText3, kit.Sans, 0)
		}),
		layout.Rigid(kit.VGap(12)))
	for _, r := range company.Roles() {
		role := strings.TrimPrefix(r.Name, company.Prefix)
		ed := d.models.ed("role." + role)
		names := make([]string, len(roleModels))
		cur := 0
		for i, m := range roleModels {
			names[i] = m[1]
			if m[0] == ed.Text() {
				cur = i
			}
		}
		names[0] = "Its own (" + r.Model + ")"
		what, _, _ := strings.Cut(r.Description, ". ")
		kids = append(kids, row(r.Name, strings.TrimSuffix(what, ".")+".", func(gtx layout.Context) layout.Dimensions {
			return u.choice(gtx, "cfgRole"+role, names, cur, none, func(i int) { ed.SetText(roleModels[i][0]) })
		}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}
