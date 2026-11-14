package web

import (
	"encoding/json"
	"testing"
)

func TestTaskSettingsPopoverTracksDynamicContent(t *testing.T) {
	extra := map[string]any{
		"/api/projects": []params{{"id": "p", "name": "Workspace", "source": "empty"}}, "/api/environments": []any{}, "/api/personal-config": params{"config": params{}},
		"/api/sessions":                       []params{{"id": "s", "project_id": "p", "title": "Conversation"}},
		"/api/projects/p/sessions/s/settings": params{"sandbox": "write_reversible", "network_request": nil},
		"/api/projects/p/sessions/s/tasks":    []params{{"id": "done", "status": "TASK_STATUS_DONE", "message": "Question", "created_at": "2026-11-09T00:00:00Z"}},
		"/api/projects/p/sessions/s/changes":  params{"files": []any{}},
		"/api/projects/p/sessions/s/env":      params{},
		"/api/projects/p/sessions/s/snapshot": params{"tasks": []any{}},
		"/api/network-policy":                 params{"allowed": true, "modes": []string{"deny", "allowlist", "open"}, "hosts": []string{"api.example.com", "docs.example.com"}, "max_grant_minutes": 60, "supports_unlimited": true},
	}
	for _, viewport := range []string{"900,600", "390,644"} {
		t.Run(viewport, func(t *testing.T) {
			var result struct {
				Errors []string `json:"errors"`
			}
			raw := runAccountProbe(t, "en", popoverGeometryProbe, params{"sandbox_control": true}, extra, viewport)
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("dynamic geometry: %v %s", err, raw)
			}
		})
	}
}

const popoverGeometryProbe = `
const out={errors:[]};addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<300;i++){const v=fn();if(v)return v;await new Promise(r=>setTimeout(r,20));}throw Error(label);};
const check=(v,m)=>{if(!v)throw Error(m);};
const footerVisible=()=>{
 const pop=$('.task-settings-pop');if(!pop)return false;const r=pop.getBoundingClientRect();
 if(r.top<7||r.left<7||r.bottom>innerHeight-7||r.right>innerWidth-7)return false;
 return $$('.task-settings-actions button').every(button=>{const box=button.getBoundingClientRect(),hit=document.elementFromPoint(box.x+box.width/2,box.y+box.height/2);return box.top>=r.top&&box.bottom<=r.bottom&&!!hit&&(hit===button||button.contains(hit));});
};
try{
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
 for(const theme of ['light','dark']){
  document.documentElement.dataset.theme=theme;
  const anchor=await wait(()=>$('.task-settings-trigger'),'anchor');check(anchor.getBoundingClientRect().top>innerHeight*.65,'test anchor must be close to viewport bottom');anchor.click();
  await wait(footerVisible,'initial footer '+theme);const initial=$('.task-settings-pop').getBoundingClientRect();
  $('[data-network-mode="allowlist"]').click();await wait(footerVisible,'expanded hosts footer '+theme);
  const scroller=$('.task-settings-scroll'),expanded=$('.task-settings-pop').getBoundingClientRect();
  check(expanded.top<initial.top-10||scroller.scrollHeight>scroller.clientHeight,'content growth was not repositioned or made scrollable');
  const hosts=$('.network-hosts-input');hosts.style.height='180px';await wait(()=>hosts.getBoundingClientRect().height>=175&&footerVisible(),'resized textarea footer '+theme);
  scroller.scrollTop=scroller.scrollHeight;await wait(footerVisible,'footer after internal scrolling '+theme);
  scroller.scrollTop=0;hosts.style.height='100px';await wait(()=>hosts.getBoundingClientRect().height<110&&footerVisible(),'shrunk textarea footer '+theme);
  for(const mode of ['open','deny','allowlist','open']){
   scroller.scrollTop=0;$('[data-network-mode="'+mode+'"]').click();await wait(footerVisible,'mode '+mode+' footer '+theme);
  }
  const duration=$('.network-duration-field .network-duration');check([...duration.options].some(o=>o.value==='0'),'unlimited option missing');duration.value='0';duration.dispatchEvent(new Event('change'));
  await wait(()=>!$('.network-duration-hint').hidden&&footerVisible(),'unlimited hint footer '+theme);
  check($('.network-duration-hint').textContent.includes('task finishes'),'unlimited must stay task scoped');
  $('.task-settings-actions button').click();await wait(()=>!$('.task-settings-pop'),'cancel closes popover');
 }
}catch(e){out.errors.push(String(e.stack||e));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`
