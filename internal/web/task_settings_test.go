package web

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

func TestTaskSettingsRespectNetworkPolicyInBrowser(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		allowed bool
		modes   []string
		scope   string
	}{{"denied", false, []string{"deny"}, ""}, {"hosts", true, []string{"deny", "allowlist"}, "hosts"}, {"public", true, []string{"deny", "allowlist", "open"}, "public"}} {
		t.Run(scenario.name, func(t *testing.T) {
			var mu sync.Mutex
			var task params
			extra := map[string]any{
				"/api/projects": []params{{"id": "p", "name": "Workspace", "source": "empty"}}, "/api/personal-config": params{"config": params{}}, "/api/environments": []any{},
				"/api/network-policy": params{"allowed": scenario.allowed, "modes": scenario.modes, "hosts": []string{"a.example.com", "b.example.com"}, "max_grant_minutes": 60},
				"POST /api/projects/p/sessions": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_ = json.NewEncoder(w).Encode(params{"id": "s", "project_id": "p", "title": "Network task"})
				}),
				"POST /api/projects/p/sessions/s/tasks": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					_ = json.NewDecoder(r.Body).Decode(&task)
					_ = json.NewEncoder(w).Encode(params{"id": "network-task", "status": "TASK_STATUS_QUEUED", "message": "Check documentation"})
				}),
				"/api/projects/p/sessions/s/changes": params{"files": []any{}}, "/api/projects/p/sessions/s/env": params{},
			}
			args, _ := json.Marshal(params{"scope": scenario.scope, "allowed": scenario.allowed})
			var result struct {
				Errors []string `json:"errors"`
			}
			raw := runAccountProbe(t, "en", "const C="+string(args)+";\n"+taskSettingsProbe, params{"sandbox_control": true}, extra, "390,740")
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("UI: %v %s", err, raw)
			}
			mu.Lock()
			defer mu.Unlock()
			network, _ := task["network_request"].(map[string]any)
			if scenario.scope == "" {
				if network != nil {
					t.Fatalf("denied policy produced network grant: %v", task)
				}
			} else {
				if network["scope"] != scenario.scope || network["minutes"] != float64(30) {
					t.Fatalf("network request: %v", task)
				}
				hosts := network["hosts"].([]any)
				if scenario.scope == "hosts" && len(hosts) != 2 || scenario.scope == "public" && len(hosts) != 0 {
					t.Fatalf("host scope: %v", network)
				}
			}
		})
	}
}

const taskSettingsProbe = `
const out={errors:[]};addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<350;i++){const v=fn();if(v)return v;await new Promise(r=>setTimeout(r,25));}throw Error(label+' '+document.body.innerText.slice(-1300));};
const check=(v,m)=>{if(!v)throw Error(m);};
try{
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/new';
 await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
 (await wait(()=>$('.task-settings-trigger'),'composer settings')).click();
 await wait(()=>$('.task-settings-panel'),'settings panel');
 check($$('.c-tools > .tool').length===2,'primary toolbar must only contain model and task settings');
 check(!!$('.composer-footer .c-hint'),'keyboard hints need their own row');
 check($('.network-duration-field').hidden,'offline mode must hide grant duration');
 check($('[data-network-mode="open"]').disabled===(C.scope!=='public'),'public scope ignores policy');
 if(!C.allowed){check($('[data-network-mode="allowlist"]').disabled,'disabled policy exposes host access');}
 else {
  $('[data-network-mode="'+(C.scope==='public'?'open':'allowlist')+'"]').click();
  check(!$('.network-duration-field').hidden,'grant duration missing');
  if(C.scope==='hosts'){
   const hosts=$('.network-hosts-input');check(hosts.tagName==='TEXTAREA'&&hosts.rows>=3,'hosts need editable multiline field');
   hosts.value='a.example.com\nb.example.com';check(hosts.getBoundingClientRect().width>220,'hosts input truncated');
  }else check($('.network-hosts-field').hidden,'public mode does not need hosts');
  $('.network-duration-field select').value='30';
 }
 check($('.task-settings-pop').getBoundingClientRect().right<=innerWidth,'popover exceeds viewport');
 $('[data-task-settings-apply]').click();await wait(()=>!$('.task-settings-pop'),'applied settings');
 $('#composer').value='Check documentation';$('#composer').dispatchEvent(new Event('input',{bubbles:true}));$('.send').click();
 await wait(()=>$('#turn-network-task'),'task submission');
}catch(e){out.errors.push(String(e.stack||e));}await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`
