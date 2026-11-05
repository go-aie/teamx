package gitlab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

type referenceObject struct {
	ID        int64  `json:"id"`
	IID       int64  `json:"iid"`
	ProjectID int64  `json:"project_id"`
	WebURL    string `json:"web_url"`
}
type referenceDiscussion struct {
	ID         string `json:"id"`
	Individual bool   `json:"individual_note"`
	Notes      []struct {
		ID int64 `json:"id"`
	} `json:"notes"`
}
type referenceLookup struct {
	object   referenceObject
	notes    map[platform.ExternalID]referenceMapping
	complete bool
	failure  error
}
type referenceMapping struct {
	discussion platform.ExternalID
	individual bool
}
type resolutionFailure struct{ code, reason string }

func (e *resolutionFailure) Error() string { return e.reason }
func referenceFailure(step string, err error) (string, string) {
	var f *resolutionFailure
	if errors.As(err, &f) {
		return f.code, step + "：" + f.reason
	}
	var q *queryError
	if errors.As(err, &q) {
		code := q.Code
		if q.Status == 401 || q.Status == 403 {
			code = "permission_denied"
		}
		if q.Status != 0 {
			return code, fmt.Sprintf("%s失败（HTTP %d）；未证明目标不存在", step, q.Status)
		}
		if code == "invalid_response" {
			return code, step + "响应无效"
		}
	}
	if errors.Is(err, context.Canceled) {
		return "canceled", step + "已取消"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", step + "超过时限"
	}
	return "query_failed", step + "失败；未获得完整证据"
}
func positiveREST(id platform.ExternalID, namespace string) bool {
	n, err := strconv.ParseInt(id.Value, 10, 64)
	return id.Namespace == "gitlab:rest:"+namespace && err == nil && n > 0 && strconv.FormatInt(n, 10) == id.Value
}
func referenceError(code, reason string) error { return &resolutionFailure{code, reason} }

// ResolveTarget uses the selected member's admitted access. It owns no Room state
// and does not discover credentials or execute Agent tools.
func ResolveTarget(ctx context.Context, locator platform.TargetLocator, access platform.AccessContext, read ReadAPI) (platform.TargetResolution, error) {
	results, err := ResolveTargets(ctx, []platform.TargetLocator{locator}, access, read)
	if err != nil {
		return platform.TargetResolution{}, err
	}
	return results[0], nil
}

// ResolveTargets shares only this call's account/object evidence. No persistent
// cache or background reads are introduced. Bounds: 25 inputs, 5 objects, 5
// object pages, 10 discussion pages / 500 notes, 1 MiB per response, 30s total.
func ResolveTargets(ctx context.Context, locators []platform.TargetLocator, access platform.AccessContext, read ReadAPI) ([]platform.TargetResolution, error) {
	if len(locators) == 0 || len(locators) > 25 {
		return nil, platform.ErrReference
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cache := map[string]referenceLookup{}
	objects := 0
	userRead := false
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	var userErr error
	get := func(path string, query url.Values, out any) (http.Header, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if read == nil {
			return nil, referenceError("capability_unavailable", "内部读取能力不可用")
		}
		response, err := read(ctx, path, query, nil, 1<<20)
		if err != nil {
			return nil, err
		}
		if len(response.Data) > 1<<20 || decodeJSON(response.Data, out) != nil {
			return nil, referenceError("invalid_response", "响应无效或超过大小限制")
		}
		return response.Header, nil
	}
	results := make([]platform.TargetResolution, 0, len(locators))
	for _, locator := range locators {
		r := platform.TargetResolution{Status: platform.ResolutionUnsupported, ReasonCode: "unsupported", Reason: "GitLab 解析需要通知来源证据", CheckedAt: time.Now().UTC()}
		if locator.Source == nil || locator.Candidate != nil {
			results = append(results, r)
			continue
		}
		source := *locator.Source
		ref := source.Reference
		expected := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "gitlab", Instance: source.Instance}, ID: ref.RecipientID}
		if !validOrigin(source.Instance) || source.Provider != "gitlab" || source.Tenant != (platform.ExternalID{}) || !access.Valid() || access.Account != expected || access.Receiver != expected || !positiveREST(ref.RecipientID, "id") {
			r.Status, r.ReasonCode, r.Reason = platform.ResolutionMismatch, "account_mismatch", "来源实例或接收账号与当前连接不符"
			results = append(results, r)
			continue
		}
		resource := map[string]string{"MergeRequest": "merge_requests", "Issue": "issues"}[ref.Object.Type]
		if !ref.Valid() || ref.Version != 1 || resource == "" || !positiveREST(ref.ProjectID, "id") || (ref.Object.ID != (platform.ExternalID{}) && !positiveREST(ref.Object.ID, "id")) || (ref.Object.IID != (platform.ExternalID{}) && !positiveREST(ref.Object.IID, "iid")) || (!ref.Object.ID.Valid() && !ref.Object.IID.Valid()) {
			r.Reason = "需要 MR／Issue 的原生 REST 项目及对象身份；不转换 ID／IID／GraphQL 命名空间"
			results = append(results, r)
			continue
		}
		if locator.URL != "" && locator.URL != source.URL {
			r.Status, r.ReasonCode, r.Reason = platform.ResolutionMismatch, "locator_mismatch", "候选 URL 与来源不符"
			results = append(results, r)
			continue
		}
		target := platform.TargetIdentity{IdentityScope: expected.IdentityScope, Containers: []platform.NativeIdentity{{Type: "Project", ID: ref.ProjectID}}, Object: platform.NativeIdentity{Type: ref.Object.Type, ID: ref.Object.ID}}
		// Built-in source native IDs already establish object identity. Optional
		// discussion work must not erase it on permission/query/pagination failure.
		if positiveREST(ref.Object.ID, "id") {
			r.Target = &target
			r.Account = access.Account
			r.ObjectVerified = true
		}
		setAlias := func(iid platform.ExternalID) {
			if positiveREST(iid, "iid") && r.Target != nil {
				alias := *r.Target
				alias.Object.ID = iid
				r.Aliases = []platform.TargetIdentity{alias}
			}
		}
		if r.ObjectVerified && !ref.NoteID.Valid() && !ref.DiscussionID.Valid() && !strings.Contains(source.URL, "#") {
			setAlias(ref.Object.IID)
			r.Status, r.ReasonCode, r.Reason, r.Complete = platform.ResolutionResolved, "", "", true
			results = append(results, r)
			continue
		}
		fail := func(step string, err error) {
			r.ReasonCode, r.Reason = referenceFailure(step, err)
			r.Status = platform.ResolutionUnavailable
			if r.ObjectVerified {
				r.Status = platform.ResolutionPartial
			}
		}
		if !userRead {
			_, userErr = get("/user", nil, &user)
			userRead = true
			if userErr == nil && (user.ID <= 0 || !platform.ValidUsername(user.Username)) {
				userErr = referenceError("invalid_response", "账号响应无效")
			}
		}
		if userErr != nil {
			fail("账号读取", userErr)
			results = append(results, r)
			continue
		}
		if restID(user.ID, "id") != expected.ID {
			r = platform.TargetResolution{Status: platform.ResolutionMismatch, ReasonCode: "account_mismatch", Reason: "API 账号与来源接收账号不符", CheckedAt: r.CheckedAt}
			results = append(results, r)
			continue
		}
		base := "/projects/" + ref.ProjectID.Value + "/" + resource
		identity := ref.Object.IID
		if !identity.Valid() {
			identity = ref.Object.ID
		}
		key := base + "/" + identity.Namespace + "/" + identity.Value
		lookup, found := cache[key]
		if !found {
			if objects >= 5 {
				fail("对象定位", referenceError("incomplete", "每批对象查询预算已用尽"))
				results = append(results, r)
				continue
			}
			objects++
			var err error
			if ref.Object.IID.Valid() {
				_, err = get(base+"/"+ref.Object.IID.Value, nil, &lookup.object)
			} else {
				err = referenceError("incomplete", "有界对象分页未定位原生 ID；未证明对象不存在")
				for page := 1; page <= 5; page++ {
					var rows []referenceObject
					h, e := get(base, url.Values{"scope": {"all"}, "state": {"all"}, "per_page": {"50"}, "page": {strconv.Itoa(page)}}, &rows)
					if e != nil {
						err = e
						break
					}
					if len(rows) > 50 {
						err = referenceError("invalid_response", "对象响应超过条数限制")
						break
					}
					for _, row := range rows {
						if restID(row.ID, "id") == ref.Object.ID {
							lookup.object = row
							err = nil
							break
						}
					}
					if err == nil {
						break
					}
					next, e := referenceNextPage(h, page, len(rows))
					if e != nil {
						err = e
						break
					}
					if next == 0 {
						break
					}
				}
			}
			if err == nil {
				obj := lookup.object
				if obj.ID <= 0 || obj.IID <= 0 || obj.ProjectID <= 0 || obj.WebURL == "" {
					err = referenceError("invalid_response", "对象响应缺少有效原生字段")
				} else if restID(obj.ProjectID, "id") != ref.ProjectID || !objectURLMatches(source.Instance, obj.WebURL, resource, obj.IID) {
					err = referenceError("identity_conflict", "API 对象身份或 URL 与来源不符")
				}
			}
			lookup.failure = err
			cache[key] = lookup
			if err == nil {
				cache[base+"/gitlab:rest:id/"+strconv.FormatInt(lookup.object.ID, 10)] = lookup
				cache[base+"/gitlab:rest:iid/"+strconv.FormatInt(lookup.object.IID, 10)] = lookup
			}
		}
		obj := lookup.object
		conflict := lookup.failure == nil && ((ref.Object.ID.Valid() && restID(obj.ID, "id") != ref.Object.ID) || (ref.Object.IID.Valid() && restID(obj.IID, "iid") != ref.Object.IID))
		var f *resolutionFailure
		if conflict || (errors.As(lookup.failure, &f) && f.code == "identity_conflict") {
			r = platform.TargetResolution{Status: platform.ResolutionMismatch, ReasonCode: "identity_conflict", Reason: "对象原生身份冲突；未执行 note 查找", CheckedAt: r.CheckedAt}
			results = append(results, r)
			continue
		}
		if lookup.failure != nil {
			fail("对象读取", lookup.failure)
			results = append(results, r)
			continue
		}
		target.Object.ID = restID(obj.ID, "id")
		r.Target = &target
		r.Account = access.Account
		r.ObjectVerified = true
		r.URL = obj.WebURL
		setAlias(restID(obj.IID, "iid"))
		if !ref.NoteID.Valid() && !ref.DiscussionID.Valid() && !strings.Contains(source.URL, "#") {
			r.Status, r.ReasonCode, r.Reason, r.Complete = platform.ResolutionResolved, "", "", true
			results = append(results, r)
			continue
		}

		if lookup.notes == nil {
			lookup.notes = map[platform.ExternalID]referenceMapping{}
			seen := map[string]bool{}
			for page := 1; page <= 10; page++ {
				var rows []referenceDiscussion
				h, err := get(base+"/"+strconv.FormatInt(obj.IID, 10)+"/discussions", url.Values{"per_page": {"50"}, "page": {strconv.Itoa(page)}}, &rows)
				if err != nil {
					lookup.failure = err
					break
				}
				valid := len(rows) <= 50
				for _, d := range rows {
					if !valid || strings.TrimSpace(d.ID) == "" || len(d.ID) > 4096 || strings.ContainsAny(d.ID, "/\\?#\r\n\x00") || seen[d.ID] || len(d.Notes) == 0 {
						valid = false
						break
					}
					seen[d.ID] = true
					for _, n := range d.Notes {
						id := restID(n.ID, "id")
						if n.ID <= 0 || lookup.notes[id].discussion.Valid() || len(lookup.notes) >= 500 {
							valid = false
							break
						}
						lookup.notes[id] = referenceMapping{platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: d.ID}, d.Individual}
					}
				}
				if !valid {
					lookup.notes = map[platform.ExternalID]referenceMapping{}
					lookup.failure = referenceError("invalid_response", "讨论响应无效、重复或超过限制")
					break
				}
				next, err := referenceNextPage(h, page, len(rows))
				if err != nil {
					lookup.failure = err
					break
				}
				if next == 0 {
					lookup.complete = true
					break
				}
				if page == 10 {
					lookup.failure = referenceError("incomplete", "讨论分页未完成（最多 10 页／500 条 note）")
				}
			}
			cache[base+"/gitlab:rest:id/"+strconv.FormatInt(obj.ID, 10)] = lookup
			cache[base+"/gitlab:rest:iid/"+strconv.FormatInt(obj.IID, 10)] = lookup
		}
		r.Status, r.ReasonCode, r.Reason, r.Complete = platform.ResolutionResolved, "", "", lookup.complete
		if lookup.failure != nil {
			fail("讨论读取", lookup.failure)
		}
		// Incomplete enumeration never creates a discussion reply target, even if
		// the requested note happened to occur on an earlier page.
		if !lookup.complete {
			results = append(results, r)
			continue
		}
		note := ref.NoteID
		if !note.Valid() {
			note = verifiedURLNote(source.URL, obj.WebURL)
		}
		if !note.Valid() && (ref.DiscussionID.Valid() || strings.Contains(source.URL, "#")) {
			fail("note 定位", referenceError("incomplete", "缺少已核实的 note 映射"))
			r.Complete = false
		}
		if note.Valid() {
			mapping, ok := lookup.notes[note]
			switch {
			case !positiveREST(note, "id"):
				fail("note 定位", referenceError("unsupported", "note 不是原生 REST ID"))
				r.Complete = false
			case !ok:
				fail("note 定位", referenceError("incomplete", "返回讨论中未找到 note；未证明其不存在"))
				r.Complete = false
			case ref.DiscussionID.Valid() && ref.DiscussionID != mapping.discussion:
				fail("讨论核验", referenceError("identity_conflict", "来源 discussion 与 note 映射冲突"))
				r.Complete = false
			default:
				target.Message = note
				r.MessageVerified = true
				if !mapping.individual {
					target.Discussion = mapping.discussion
					r.DiscussionVerified = true
				}
				setAlias(restID(obj.IID, "iid"))
			}
		}
		results = append(results, r)
	}
	return results, nil
}

func referenceNextPage(h http.Header, page, count int) (int, error) {
	values, present := h["X-Next-Page"]
	if len(values) > 1 {
		return 0, referenceError("invalid_response", "分页边界无效")
	}
	next := ""
	if len(values) == 1 {
		next = values[0]
	}
	if next == "" {
		if count == 50 && !present {
			return 0, referenceError("incomplete", "满页缺少分页边界，分页未完成")
		}
		return 0, nil
	}
	if next != strconv.Itoa(page+1) {
		return 0, referenceError("incomplete", "分页边界无效，分页未完成")
	}
	return page + 1, nil
}
func objectURLMatches(origin, raw, resource string, iid int64) bool {
	u, e := url.Parse(raw)
	if e != nil || len(raw) > 4096 || u.User != nil || u.Scheme+"://"+strings.ToLower(u.Host) != origin || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	suffix := "/" + strconv.FormatInt(iid, 10)
	// The Issues REST endpoint can return the unified Work Item web route.
	// This is only a URL shape check: project, native ID and IID still have to
	// match the API object above. It does not convert Work Item/GraphQL IDs.
	return strings.HasSuffix(u.Path, "/-/"+resource+suffix) || resource == "issues" && strings.HasSuffix(u.Path, "/-/work_items"+suffix)
}
func verifiedURLNote(raw, objectURL string) platform.ExternalID {
	u, e := url.Parse(raw)
	obj, oe := url.Parse(objectURL)
	if e != nil || oe != nil || u.User != nil || u.Scheme != obj.Scheme || !strings.EqualFold(u.Host, obj.Host) || u.EscapedPath() != obj.EscapedPath() || u.RawQuery != "" || u.ForceQuery || !strings.HasPrefix(u.Fragment, "note_") {
		return platform.ExternalID{}
	}
	id := platform.ExternalID{Namespace: "gitlab:rest:id", Value: strings.TrimPrefix(u.Fragment, "note_")}
	if !positiveREST(id, "id") {
		return platform.ExternalID{}
	}
	return id
}
