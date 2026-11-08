package web

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestChatTopbarKeepsContextCompactAndActionsReachable(t *testing.T) {
	const projectName = "Customer analytics and billing workspace with a deliberately long descriptive name"
	const sessionTitle = "Investigate the cross-repository billing regression and review the final changes"
	const base = "/api/projects/p/sessions/s"
	repos := []params{{"name": "customer-facing-web-application", "path": "web", "url": "https://github.com/example/customer-facing-web-application", "access": "REPO_ACCESS_WRITE", "primary": true, "state": params{"branch": "agent/long-feature-branch-name"}}, {"name": "backend-billing-and-payments-service", "path": "api", "url": "https://github.com/example/backend-billing-and-payments-service", "access": "REPO_ACCESS_READ"}}
	extra := map[string]any{
		"/api/projects":     []params{{"id": "p", "name": projectName, "layout": "WORKSPACE_LAYOUT_MULTI"}},
		"/api/sessions":     []params{{"id": "s", "project_id": "p", "title": sessionTitle, "workspace_id": "session-copy"}},
		"/api/environments": []any{}, "/api/personal-config": params{"config": params{}}, "/api/skills": params{"available": true, "skills": []any{}}, "/api/mcp": params{"available": true, "connections": []any{}},
		base + "/tasks": []any{}, base + "/repos": params{"repos": repos}, base + "/env": params{}, base + "/snapshot": params{"tasks": []any{}},
		"GET " + base + "/changes": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(params{"files": []any{}, "repo": r.URL.Query().Get("repo")})
		}),
		"/api/projects/p/repos": params{"repos": repos},
	}
	for _, scenario := range []struct{ language, viewport string }{{"en", "1440,900"}, {"en", "900,600"}, {"en", "390,744"}, {"zh", "390,744"}} {
		t.Run(scenario.language+scenario.viewport, func(t *testing.T) {
			var result struct {
				Errors []string `json:"errors"`
			}
			raw := runAccountProbe(t, scenario.language, topbarProbe, params{}, extra, scenario.viewport)
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("topbar: %v %s", err, raw)
			}
		})
	}
}

const topbarProbe = `
const out={errors:[]};addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<300;i++){const v=fn();if(v)return v;await new Promise(r=>setTimeout(r,25));}throw Error(label+' '+document.body.innerText.slice(-1600));};
const check=(v,m)=>{if(!v)throw Error(m);};
try{
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';
 await wait(()=>$('[data-repo-picker]'),'repository selector');
 const bar=$('.topbar'),r=bar.getBoundingClientRect(),title=$('.topbar-context .title');
 check(r.height===44,'chat header must stay 44px tall');
 const statusHead=$('.status-head')?.getBoundingClientRect();
 if(statusHead?.height)check(statusHead.height===44,'status header does not align with chat header');
 const titleRect=title.getBoundingClientRect(),crumbRect=$('.topbar-context .crumb').getBoundingClientRect();
 check(Math.abs((titleRect.top+titleRect.bottom)/2-(r.top+r.bottom)/2)<=1,'title not vertically centered');
 if(innerWidth>760)check(Math.abs((titleRect.top+titleRect.bottom)/2-(crumbRect.top+crumbRect.bottom)/2)<=1,'context wraps into two rows');
 check(bar.scrollWidth<=bar.clientWidth&&r.right<=innerWidth,'header horizontal overflow');
 check(title.getBoundingClientRect().width>=36,'session title squeezed away');
 check(title.title.includes('Investigate')&&$('.topbar-context .crumb').title.includes('Customer analytics'),'full context lost');
 check(!$('.topbar .chips')&&!$('.topbar [data-repo-manage]'),'secondary metadata still consumes header space');
 check($$('.topbar-actions button').length===3,'unexpected header action count');
 for(const control of $$('.topbar-actions button')){const b=control.getBoundingClientRect();check(b.width>=32&&b.height>=32&&b.top>=r.top&&b.bottom<=r.bottom,'action hit area clipped');}
 $('[data-topbar-menu]').click();
 await wait(()=>$('.topbar-menu [data-repo-manage]'),'repository management in menu');
 check(!!$('.topbar-menu [data-topbar-drawer="files"]')&&!!$('.topbar-menu [data-topbar-drawer="history"]'),'file/history shortcuts missing');
 check(!!$('.topbar-menu [data-topbar-source]'),'repository link missing');
 $('.topbar-menu [data-repo-manage]').click();
 const manager=(await wait(()=>$('dialog[open] [data-workspace-repos]'),'repository manager')).closest('dialog');
 manager.querySelector('.d-foot button').click();
 await wait(()=>!manager.isConnected || !manager.open,'manager closed');
 const picker=$('[data-repo-picker]');picker.value='backend-billing-and-payments-service';picker.dispatchEvent(new Event('change'));
 await wait(()=>$('[data-repo-picker]').value==='backend-billing-and-payments-service','repository scope switched');
 check($('.topbar').scrollWidth<=$('.topbar').clientWidth,'read-only repository overflows');
 location.hash='#/settings/personal';await wait(()=>$('.settings-page'),'settings');
 check(!$('.topbar .title')&&$$('.settings-page h1').length===1,'settings heading duplicated in topbar');
 if(innerWidth>760)check($('.topbar').getBoundingClientRect().height===0,'empty desktop page header should not reserve a row');
 else check($('.topbar .only-narrow').getBoundingClientRect().height>0,'mobile sidebar control lost');
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`
