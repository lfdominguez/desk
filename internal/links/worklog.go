package links

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/lfdominguez/desk/internal/kanban"
	"github.com/lfdominguez/desk/internal/orgs"
)

// LogWork records d, the time a card's Claude worked, on ticket l when its
// account asks for it (worklog = true): a Jira worklog, or a comment on a
// Linear issue (Linear has no worklog). logged false: nothing to do there.
func LogWork(ctx context.Context, l kanban.Link, d time.Duration, note string, cfg orgs.Config) (logged bool, err error) {
	d = max(d.Round(time.Minute), time.Minute) // Jira takes whole minutes, one at least
	switch l.Kind {
	case KindJira:
		u, err := url.Parse(l.URL)
		if err != nil {
			return false, err
		}
		j, ok := jiraFor(cfg, u.Hostname(), l.Ref)
		if !ok || !j.Worklog {
			return false, nil
		}
		a, err := auth(ctx, cfg, j.Email, j.Secret)
		if err != nil {
			return false, err
		}
		base := strings.TrimRight(j.URL, "/")
		if base == "" {
			base = u.Scheme + "://" + u.Host
		}
		comment := map[string]any{"type": "doc", "version": 1, "content": []any{map[string]any{"type": "paragraph",
			"content": []any{map[string]any{"type": "text", "text": note}}}}}
		in := map[string]any{"timeSpentSeconds": int(d.Seconds()), "comment": comment}
		return true, doJSON(ctx, "POST", base+"/rest/api/3/issue/"+url.PathEscape(l.Ref)+"/worklog", a, in, nil)
	case KindLinear:
		acct, ok := linearAcct(cfg, l)
		if !ok || !acct.Worklog {
			return false, nil
		}
		t, err := openLinear(ctx, cfg, acct, l.Ref)
		if err != nil {
			return false, err
		}
		var v struct {
			CommentCreate struct {
				Success bool `json:"success"`
			} `json:"commentCreate"`
		}
		body := fmt.Sprintf("Time spent: %s. %s", Spent(d), note)
		err = gql(ctx, t.a, `mutation($id: String!, $body: String!) { commentCreate(input: {issueId: $id, body: $body}) { success } }`,
			map[string]any{"id": t.ID, "body": body}, &v)
		if err == nil && !v.CommentCreate.Success {
			err = fmt.Errorf("linear: the comment on %s wasn't made", l.Ref)
		}
		return true, err
	}
	return false, nil
}

// Spent is d as people write it: "1h 20m", "45m".
func Spent(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dm", m)
}
