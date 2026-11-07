package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

type UserApplication struct {
	RemoteID     int64    `json:"id"`
	ClientID     string   `json:"application_id"`
	Name         string   `json:"application_name"`
	RedirectURI  string   `json:"callback_url"`
	Confidential bool     `json:"confidential"`
	Scopes       []string `json:"-"` // GitLab versions may omit scopes in application responses.
}

// Decode optional scope without assuming that the documented list response has it.
func (a *UserApplication) UnmarshalJSON(data []byte) error {
	type plain UserApplication
	var v struct {
		plain
		Scopes json.RawMessage `json:"scopes"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*a = UserApplication(v.plain)
	if len(v.Scopes) > 0 && string(v.Scopes) != "null" {
		var list []string
		if json.Unmarshal(v.Scopes, &list) != nil {
			var scope string
			if json.Unmarshal(v.Scopes, &scope) != nil {
				return fmt.Errorf("invalid scopes")
			}
			list = strings.Fields(scope)
		}
		var ok bool
		a.Scopes, ok = platform.NormalizeScopes(list)
		if !ok {
			return fmt.Errorf("invalid scopes")
		}
	}
	return nil
}

// OwnedApplications reads only the authenticated user's personal applications.
func OwnedApplications(ctx context.Context, origin string, token platform.Secret, transport http.RoundTripper) ([]UserApplication, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	read := NewReadAPI(origin, token, transport)
	var result []UserApplication
	seenIDs, seenClients := map[int64]bool{}, map[string]bool{}
	for page, pages := 1, 0; page != 0; pages++ {
		if pages >= 100 {
			return nil, fmt.Errorf("application pagination incomplete")
		}
		response, err := read(ctx, "/user/applications", url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}, nil, 1<<20)
		if err != nil {
			return nil, err
		}
		var batch []UserApplication
		if decodeJSON(response.Data, &batch) != nil || batch == nil || len(batch) > 100 {
			return nil, fmt.Errorf("invalid applications response")
		}
		for _, a := range batch {
			if a.RemoteID <= 0 || !validToken(a.ClientID) || a.Name == "" || a.RedirectURI == "" || seenIDs[a.RemoteID] || seenClients[a.ClientID] {
				return nil, fmt.Errorf("invalid applications response")
			}
			seenIDs[a.RemoteID], seenClients[a.ClientID] = true, true
			result = append(result, a)
		}
		page, err = nextPage(response.Header, page, 100, len(batch))
		if err != nil {
			return nil, fmt.Errorf("application pagination incomplete")
		}
	}
	return result, nil
}
