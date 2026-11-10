//go:build integration || e2e

package lark

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchJoinedGroupMembershipPages(t *testing.T) {
	parallelClientTest(t)
	c, dir, a := testClient(t)
	script := strings.Replace(fixture, "assert a[1]=='GET'", `
if path.endswith('/members'):
 assert a[1]=='GET' and a[-4:]==['--as','user','--format','json']
 p=json.loads(a[a.index('--params')+1]);assert p['page_size']==100 and p['member_id_type']=='open_id'
 token=p.get('page_token','')
 data={'items':[{'member_id':'ou_reader' if token else 'ou_other','member_id_type':'open_id','tenant_key':'account_tenant'}],'has_more':not bool(token)}
 if data['has_more']:data['page_token']='next'
 if mode=='missing':del data['has_more']
 if mode=='wrong_tenant':data['items'][0]['tenant_key']='wrong'
 if mode=='loop':data['has_more']=True;data['page_token']=token
 if mode=='duplicate':data['items'].append(data['items'][0])
 if mode=='switch':open(base+'/read','w').write('yes')
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
assert a[1]=='GET'`, 1)
	if err := os.WriteFile(c.profile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	first, err := c.GroupMembership(t.Context(), a, "oc_group", "")
	if err != nil || first.Joined || !first.HasMore || first.PageToken != "next" {
		t.Fatal(first, err)
	}
	second, err := c.GroupMembership(t.Context(), a, "oc_group", first.PageToken)
	if err != nil || !second.Joined || second.HasMore {
		t.Fatal(second, err)
	}
	for _, mode := range []string{"missing", "wrong_tenant", "loop", "duplicate", "switch"} {
		setMode(t, dir, mode)
		page, err := c.GroupMembership(t.Context(), a, "oc_group", "next")
		if err == nil || page.Joined {
			t.Fatal("invalid membership proof escaped", mode, page, err)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Count(string(calls), "/oc_group/members") != 7 || strings.Contains(string(calls), "--page-all") {
		t.Fatal("membership expanded beyond one page")
	}
}
