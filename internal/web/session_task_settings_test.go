package web

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestSessionTaskSettingsSurviveSubmitSwitchRefreshAndLateSave(t *testing.T) {
	for _, viewport := range []string{"1100,800", "390,740"} {
		t.Run(viewport, func(t *testing.T) {
			var mu sync.Mutex
			settings := map[string]params{"a": {"sandbox": "write_reversible", "network_request": nil}, "b": {"sandbox": "write_reversible", "network_request": nil}}
			var submits []params
			tasks := map[string][]params{"a": {}, "b": {}}
			extra := map[string]any{
				"/api/projects":        []params{{"id": "p", "name": "P", "source": "empty"}, {"id": "q", "name": "Q", "source": "empty"}},
				"/api/sessions":        []params{{"id": "a", "project_id": "p", "title": "Session A"}, {"id": "b", "project_id": "p", "title": "Session B"}},
				"/api/personal-config": params{"config": params{}}, "/api/environments": []params{{"id": "env-a", "name": "Env A", "network": params{"mode": "open"}}, {"id": "env-b", "name": "Env B", "network": params{"mode": "allowlist", "allowed_hosts": []string{"docs.example.com"}}}},
				"/api/network-policy": params{"allowed": true, "modes": []string{"deny", "allowlist", "open"}, "hosts": []string{"github.com"}, "max_grant_minutes": 60, "supports_unlimited": true},
				"GET /api/projects/p/sessions/{session}/settings": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					json.NewEncoder(w).Encode(settings[r.PathValue("session")])
				}),
				"PUT /api/projects/p/sessions/{session}/settings": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var v params
					json.NewDecoder(r.Body).Decode(&v)
					if n, ok := v["network_request"].(map[string]any); ok && n["minutes"] == float64(30) {
						time.Sleep(350 * time.Millisecond)
					}
					mu.Lock()
					settings[r.PathValue("session")] = v
					mu.Unlock()
					json.NewEncoder(w).Encode(v)
				}),
				"GET /api/projects/p/sessions/{session}/tasks": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					json.NewEncoder(w).Encode(tasks[r.PathValue("session")])
				}),
				"POST /api/projects/p/sessions/{session}/tasks": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var v params
					json.NewDecoder(r.Body).Decode(&v)
					mu.Lock()
					defer mu.Unlock()
					v["session"] = r.PathValue("session")
					submits = append(submits, v)
					task := params{"id": "done-" + r.PathValue("session") + v["message"].(string), "status": "TASK_STATUS_DONE", "message": v["message"], "created_at": "2026-11-09T12:00:00Z"}
					tasks[r.PathValue("session")] = append(tasks[r.PathValue("session")], task)
					json.NewEncoder(w).Encode(task)
				}),
				"GET /__observed": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					json.NewEncoder(w).Encode(params{"submits": submits, "settings": settings})
				}),
			}
			for _, sid := range []string{"a", "b"} {
				base := "/api/projects/p/sessions/" + sid
				extra[base+"/changes"] = params{"files": []any{}}
				extra[base+"/env"] = params{}
				extra[base+"/repos"] = params{"repos": []any{}}
				extra[base+"/snapshot"] = params{"tasks": []any{}}
			}
			var result struct {
				Errors []string `json:"errors"`
			}
			raw := runAccountProbe(t, "en", sessionTaskSettingsProbe, params{"sandbox_control": true}, extra, viewport)
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("session settings: %v %s", err, raw)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(submits) != 3 {
				t.Fatal(submits)
			}
			for _, r := range submits[:2] {
				n, _ := r["network_request"].(map[string]any)
				if r["session"] != "a" || r["sandbox"] != "read" || n["scope"] != "public" || n["minutes"] != float64(15) {
					t.Fatal(r)
				}
			}
			if submits[2]["session"] != "b" || submits[2]["network_request"] != nil {
				t.Fatal(submits[2])
			}
		})
	}
}

const sessionTaskSettingsProbe = `
const out={errors:[]};addEventListener('error',e=>{if(e.message)out.errors.push(e.message)});addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s);
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const v=await fn();if(v)return v;await new Promise(r=>setTimeout(r,25));}throw Error(label+' '+document.body.innerText.slice(-1200));};
const check=(v,m)=>{if(!v)throw Error(m)};
const observed=()=>fetch('/__observed').then(r=>r.json());
const open=async()=>{(await wait(()=>$('.task-settings-trigger'),'settings trigger')).click();await wait(()=>$('.task-settings-panel'),'panel');};
const close=()=>document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
const selected=m=>$('[data-network-mode="'+m+'"]').getAttribute('aria-checked')==='true';
const apply=async()=>{$('[data-task-settings-apply]').click();await wait(()=>!$('.task-settings-panel'),'saved panel');};
const route=async(s)=>{location.hash='#/w/p/s/'+s;await wait(()=>$('.topbar-context .title')?.title==='Session '+s.toUpperCase(),'session '+s);await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));};
const send=async(message,count)=>{const input=$('#composer');input.value=message;input.dispatchEvent(new Event('input',{bubbles:true}));input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));await wait(async()=>((await observed()).submits||[]).length===count,'submitted '+count);await wait(()=>!document.querySelector('.pending-turn'),'submission settled');};
try{
 if(!sessionStorage.getItem('settings-reloaded')){
  (await wait(()=>$('dialog.account'),'account')).close();await route('a');await open();
  $('[data-network-mode="open"]').click();$('.task-settings-panel .task-sandbox').value='read';$('.network-duration-field .network-duration').value='15';await apply();
  await send('first',1);await open();check(selected('open')&&$('.network-duration-field .network-duration').value==='15','A reset to offline after first send');close();
  await send('second',2);await open();check(selected('open'),'A reset after second send');close();
  await route('b');await open();check(selected('deny'),'B inherited A public setting');check($('.task-settings-panel .task-sandbox').value==='write_reversible','B inherited A sandbox');
  $('[data-network-mode="allowlist"]').click();$('.network-hosts-input').value='docs.example.com';$('.network-duration-field .network-duration').value='60';await apply();
  await route('a');await open();check(selected('open')&&$('.network-duration-field .network-duration').value==='15','A overwritten by B');close();
  sessionStorage.setItem('settings-reloaded','1');location.reload();
 }else{
  await wait(()=>$('.task-settings-trigger'),'reloaded composer');await open();check(selected('open')&&$('.network-duration-field .network-duration').value==='15','refresh lost backend settings');close();
  await route('b');await open();check(selected('allowlist')&&$('.network-duration-field .network-duration').value==='60','B backend setting lost');$('[data-network-mode="deny"]').click();await apply();await send('offline',3);
  await route('a');await open();$('.network-duration-field .network-duration').value='30';$('[data-task-settings-apply]').click();await route('b');await new Promise(r=>setTimeout(r,450));await open();check(selected('deny'),'late save from A overwrote B');close();
  await route('a');await open();check(selected('open')&&$('.network-duration-field .network-duration').value==='30','A late save was not persisted');close();
  location.hash='#/w/p/new';await wait(()=>$('.topbar-context .title')?.title==='New session','draft P');await open();check(selected('deny'),'new draft inherited existing session settings');
  const environment=id=>{const e=$('[data-environment-select]');e.value=id;e.dispatchEvent(new Event('change'));};
  environment('env-a');check(selected('open')&&$('.network-duration-field .network-duration').value==='15','env A network did not populate');$('.network-duration-field .network-duration').value='60';$('.task-settings-panel .task-sandbox').value='read';
  environment('env-b');check(selected('allowlist')&&$('.network-hosts-input').value==='docs.example.com'&&$('.network-duration-field .network-duration').value==='15','env B did not populate independent hosts/duration');check($('.task-settings-panel .task-sandbox').value==='write_reversible','env B inherited A sandbox');
  environment('');check(selected('deny'),'default environment inherited networking');
  environment('env-a');check(selected('open')&&$('.network-duration-field .network-duration').value==='60'&&$('.task-settings-panel .task-sandbox').value==='read','env A draft values not restored');await apply();
  location.hash='#/w/q/new';await wait(()=>$('.topbar-context .crumb')?.title==='Q','draft Q');await open();check(selected('deny')&&$('[data-environment-select]').value==='','project Q inherited draft P');environment('env-a');check(selected('open')&&$('.network-duration-field .network-duration').value==='15'&&$('.task-settings-panel .task-sandbox').value==='write_reversible','project Q inherited A overrides from P');close();
  location.hash='#/w/p/new';await wait(()=>$('.topbar-context .crumb')?.title==='P','back draft P');await open();check(selected('open')&&$('.network-duration-field .network-duration').value==='60'&&$('[data-environment-select]').value==='env-a','draft P lost preferences');
  await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
 }
}catch(error){out.errors.push(String(error.stack||error));await fetch('/__result',{method:'POST',body:JSON.stringify(out)});}
`
