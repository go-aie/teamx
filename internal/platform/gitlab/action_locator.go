package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
)

type actionURL struct {
	project, iid, note, discussion, web string
}

type locatorDiscussion struct {
	ID         string          `json:"id"`
	Individual *bool           `json:"individual_note"`
	Notes      []referenceNote `json:"notes"`
}

// Parse segments once. In particular, an encoded separator or another escape
// must never turn into namespace structure in a later API/path decode.
func parseActionURL(raw, origin string) (actionURL, error) {
	var result actionURL
	bad := func() (actionURL, error) {
		return result, referenceError("unsupported_url", "需要完整 HTTPS MR 链接及准确 note_／discussion_ 锚点；不支持查询参数、额外路径或含糊编码")
	}
	if len(raw) > 4096 || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\\") || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return bad()
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || !validOrigin(origin) {
		return bad()
	}
	if !strings.EqualFold(u.Scheme+"://"+u.Host, origin) {
		return result, referenceError("instance_mismatch", "链接实例与选定账号不符；请选择该实例的准确账号入口")
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 6 || parts[0] != "" || parts[len(parts)-3] != "-" || parts[len(parts)-2] != "merge_requests" {
		return bad()
	}
	result.iid = parts[len(parts)-1]
	if !positiveREST(platform.ExternalID{Namespace: "gitlab:rest:iid", Value: result.iid}, "iid") {
		return bad()
	}
	project := parts[1 : len(parts)-3]
	encoded := make([]string, len(project))
	for i, part := range project {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || !utf8.ValidString(decoded) || strings.ContainsAny(decoded, "/\\%?#") || strings.IndexFunc(decoded, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
			return bad()
		}
		project[i], encoded[i] = decoded, url.PathEscape(decoded)
	}
	result.project = strings.Join(project, "/")
	result.web = strings.ToLower(u.Scheme+"://"+u.Host) + "/" + strings.Join(encoded, "/") + "/-/merge_requests/" + result.iid
	if strings.Contains(raw, "#") {
		// Anchors are native IDs, not URL-encoded paths or display strings.
		if u.RawFragment != "" || strings.Contains(u.Fragment, "%") {
			return bad()
		}
		switch {
		case strings.HasPrefix(u.Fragment, "note_"):
			result.note = strings.TrimPrefix(u.Fragment, "note_")
			if !positiveREST(restString(result.note, "id"), "id") {
				return bad()
			}
		case strings.HasPrefix(u.Fragment, "discussion_"):
			result.discussion = strings.TrimPrefix(u.Fragment, "discussion_")
			if !locatorDiscussionID(result.discussion) {
				return bad()
			}
		default:
			return bad()
		}
	}
	return result, nil
}

func restString(value, kind string) platform.ExternalID {
	return platform.ExternalID{Namespace: "gitlab:rest:" + kind, Value: value}
}

func locatorDiscussionID(id string) bool {
	return id != "" && validActionDiscussion(id) && utf8.ValidString(id) && !strings.ContainsAny(id, "%") && strings.IndexFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}

// ResolveActionURL reads only the selected origin. URL evidence is never a
// notification, an authorization, or a caller-provided native candidate.
func ResolveActionURL(ctx context.Context, l platform.TargetLocator, a platform.AccessContext, read ReadAPI, username string) (platform.TargetResolution, error) {
	r := platform.TargetResolution{Status: platform.ResolutionUnsupported, Account: a.Account, CheckedAt: time.Now().UTC()}
	fail := func(err error) (platform.TargetResolution, error) {
		r.ReasonCode, r.Reason = referenceFailure("目标解析", err)
		r.Status = platform.ResolutionUnavailable
		switch r.ReasonCode {
		case "unsupported_url", "unsupported":
			r.Status = platform.ResolutionUnsupported
		case "instance_mismatch", "identity_conflict", "account_mismatch":
			r.Status = platform.ResolutionMismatch
		}
		var q *queryError
		if errors.Is(err, platform.ErrCredential) {
			r.ReasonCode, r.Reason = "credential_unavailable", "当前连接凭据不可用；请核对该账号的授权状态"
		}
		if errors.As(err, &q) {
			switch q.Status {
			case 404:
				r.ReasonCode, r.Reason = "not_found_or_invisible", "目标未找到或对当前账号不可见；核对链接与账号权限"
			case 429:
				r.ReasonCode, r.Reason = "rate_limited", "GitLab 读取限流；稍后重试原链接"
			case 401, 403:
				r.Reason = "当前账号凭据或读取权限不可用；核对该连接的授权与项目访问权限"
			}
		}
		return r, nil
	}
	if l.URL == "" || l.Source != nil || l.Candidate != nil || l.Query != nil || l.Self || l.TargetRef != "" {
		return fail(referenceError("unsupported", "GitLab 独立定位仅接受完整 MR／讨论 URL"))
	}
	if !a.Valid() || a.Account != a.Receiver || a.Account.Provider != "gitlab" || !positiveREST(a.Account.ID, "id") {
		return fail(referenceError("account_mismatch", "当前绑定账号证据无效"))
	}
	locator, err := parseActionURL(l.URL, a.Account.Instance)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	get := func(path string, query url.Values, out any) (http.Header, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if read == nil {
			return nil, referenceError("capability_unavailable", "内部读取能力不可用")
		}
		response, err := read(ctx, path, query, nil, 1<<20)
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(response.Data) > 1<<20 || decodeJSON(response.Data, out) != nil {
			return nil, referenceError("invalid_response", "响应无效或超过 1 MiB")
		}
		return response.Header, nil
	}
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if _, err := get("/user", nil, &user); err != nil {
		return fail(err)
	}
	if restID(user.ID, "id") != a.Account.ID || !platform.ValidUsername(user.Username) || username != "" && user.Username != username {
		return fail(referenceError("account_mismatch", "远端账号与当前绑定账号不符"))
	}
	r.AccountLabel = user.Username
	var project struct {
		ID   int64  `json:"id"`
		Path string `json:"path_with_namespace"`
	}
	if _, err := get("/projects/"+url.PathEscape(locator.project), nil, &project); err != nil {
		return fail(err)
	}
	if project.ID <= 0 || project.Path != locator.project {
		return fail(referenceError("identity_conflict", "项目路径与 API 返回不符；不接受迁移后的其他目标"))
	}
	base := "/projects/" + strconv.FormatInt(project.ID, 10) + "/merge_requests/" + locator.iid
	var mr referenceObject
	if _, err := get(base, nil, &mr); err != nil {
		return fail(err)
	}
	verified, err := parseActionURL(mr.WebURL, a.Account.Instance)
	if err != nil || mr.ID <= 0 || mr.ProjectID != project.ID || strconv.FormatInt(mr.IID, 10) != locator.iid || verified.project != locator.project || verified.iid != locator.iid || verified.note != "" || verified.discussion != "" {
		return fail(referenceError("identity_conflict", "MR 原生身份或完整网页路径与链接不符"))
	}
	target := platform.TargetIdentity{IdentityScope: a.Account.IdentityScope, Containers: []platform.NativeIdentity{{Type: "Project", ID: restID(project.ID, "id")}}, Object: platform.NativeIdentity{Type: "MergeRequest", ID: restID(mr.ID, "id")}}
	validate := func(d locatorDiscussion, seen map[string]bool, notes map[int64]bool) error {
		if !locatorDiscussionID(d.ID) || d.Individual == nil || seen[d.ID] || len(d.Notes) == 0 || len(d.Notes) > 500 {
			return referenceError("incomplete", "讨论响应缺失、重复或超过限制")
		}
		seen[d.ID] = true
		for _, n := range d.Notes {
			if n.ID <= 0 || notes[n.ID] || len(notes) >= 500 || n.NoteableID != mr.ID || n.NoteableType != "MergeRequest" || n.NoteableIID != 0 && n.NoteableIID != mr.IID {
				return referenceError("incomplete", "note 映射冲突或超过 500 条限制")
			}
			notes[n.ID] = true
		}
		return nil
	}
	seen, notes := map[string]bool{}, map[int64]bool{}
	if locator.discussion != "" {
		var d locatorDiscussion
		if _, err := get(base+"/discussions/"+url.PathEscape(locator.discussion), nil, &d); err != nil {
			return fail(err)
		}
		if err := validate(d, seen, notes); err != nil {
			return fail(err)
		}
		if d.ID != locator.discussion || *d.Individual {
			return fail(referenceError("unsupported", "链接未指向该 MR 中可回复的准确讨论"))
		}
		target.Discussion = restString(d.ID, "discussion")
	} else if locator.note != "" {
		note, _ := strconv.ParseInt(locator.note, 10, 64)
		for page := 1; page <= 10; page++ {
			var rows []locatorDiscussion
			header, err := get(base+"/discussions", url.Values{"per_page": {"50"}, "page": {strconv.Itoa(page)}}, &rows)
			if err != nil {
				return fail(err)
			}
			if len(rows) > 50 {
				return fail(referenceError("incomplete", "讨论响应超过分页限制"))
			}
			var match *locatorDiscussion
			for _, d := range rows {
				if err := validate(d, seen, notes); err != nil {
					return fail(err)
				}
				for _, n := range d.Notes {
					if n.ID == note {
						copy := d
						match = &copy
					}
				}
			}
			next, err := referenceNextPage(header, page, len(rows))
			if err != nil {
				return fail(err)
			}
			if match != nil {
				if *match.Individual {
					return fail(referenceError("unsupported", "该 note 是独立普通评论，不能作为讨论回复；如需普通评论请明确使用无锚点 MR 链接"))
				}
				target.Discussion = restString(match.ID, "discussion")
				break
			}
			if next == 0 || page == 10 || len(notes) >= 500 {
				return fail(referenceError("incomplete", "有界讨论读取未找到准确 note；未证明其不存在"))
			}
		}
	}
	alias := target
	alias.Object.ID = restID(mr.IID, "iid")
	r.Status, r.Target, r.Aliases = platform.ResolutionResolved, &target, []platform.TargetIdentity{alias}
	r.ObjectVerified, r.DiscussionVerified, r.Complete, r.URL = true, target.Discussion.Valid(), true, locator.web
	return r, nil
}
