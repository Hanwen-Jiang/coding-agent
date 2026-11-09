package web

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestComposerKeepsEditorAndActionsInsideOneSurface(t *testing.T) {
	extra := map[string]any{
		"/api/projects":     []params{{"id": "p", "name": "Workspace", "source": "empty"}},
		"/api/sessions":     []params{{"id": "done", "project_id": "p", "title": "Completed"}, {"id": "running", "project_id": "p", "title": "Running"}, {"id": "waiting", "project_id": "p", "title": "Approval"}},
		"/api/environments": []any{}, "/api/personal-config": params{"config": params{}},
	}
	for _, mode := range []string{"done", "running", "waiting"} {
		base := "/api/projects/p/sessions/" + mode
		id, status := "surface-"+mode, "TASK_STATUS_RUNNING"
		if mode == "done" {
			status = "TASK_STATUS_DONE"
		}
		task := params{"id": id, "status": status, "message": "Check the existing tests.", "created_at": "2026-11-09T08:00:00Z", "cost_usd": 1234567.89}
		events := []params{snapshotEvent(1, id, "task_accepted", params{}), snapshotEvent(2, id, "round_started", params{"round_seq": 1})}
		if mode == "done" {
			events = append(events, snapshotEvent(3, id, "task_finished", params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": "Surface done."}}}))
		}
		if mode == "waiting" {
			events = append(events, snapshotEvent(3, id, "human_input_required", params{"question_id": "surface-approval", "question": "Please confirm whether to run the additional verification and preserve the existing changes for review.", "options": []string{"approve", "reject"}}))
		}
		extra[base+"/tasks"] = []params{task}
		extra[base+"/tasks/"+id] = params{"task": task, "runtime": params{}}
		extra[base+"/tasks/"+id+"/artifacts"] = params{"artifacts": []any{}}
		extra[base+"/env"] = params{}
		extra[base+"/changes"] = params{"files": []any{}}
		extra[base+"/snapshot"] = params{"tasks": []params{{"task_id": id, "status": mode, "last_event_id": len(events), "source": "events", "events": events}}}
		if mode != "done" {
			extra["GET "+base+"/tasks/"+id+"/events"] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
		}
	}
	for _, scenario := range []struct{ language, viewport string }{{"zh", "1440,900"}, {"zh", "390,744"}, {"en", "390,744"}} {
		t.Run(scenario.language+scenario.viewport, func(t *testing.T) {
			var result struct {
				Errors []string `json:"errors"`
			}
			raw := runAccountProbe(t, scenario.language, composerSurfaceProbe, params{}, extra, scenario.viewport)
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("composer surface: %v %s", err, raw)
			}
		})
	}
}

const composerSurfaceProbe = `
const out={errors:[]};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
addEventListener('securitypolicyviolation',e=>out.errors.push('CSP: '+e.violatedDirective+' '+e.blockedURI));
const policy=document.createElement('meta');policy.httpEquiv='Content-Security-Policy';policy.content="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'";document.head.append(policy);
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const value=fn();if(value)return value;await new Promise(r=>setTimeout(r,25));}throw Error(label);};
const frame=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
const check=(value,label)=>{if(!value)throw Error(label);};
const original=fetch.bind(window);let writes=0;
window.fetch=(url,options)=>{if(options?.method==='POST'&&/\/(tasks|guidance|reply)$/.test(String(url)))writes++;return original(url,options);};
const inside=(inner,outer)=>inner.left>=outer.left-1&&inner.right<=outer.right+1&&inner.top>=outer.top-1&&inner.bottom<=outer.bottom+1;
const inspect=mode=>{
 const shell=$('.prompt-input__shell'),editor=$('#composer'),toolbar=$('.composer-toolbar');
 const s=shell.getBoundingClientRect(),e=editor.getBoundingClientRect(),t=toolbar.getBoundingClientRect();
 check(shell.contains(toolbar)&&toolbar.matches('[data-slot="prompt-input-toolbar"]'),'toolbar must be the real Pro toolbar inside the composer surface');
 check(shell.contains($('.c-tools'))&&inside(t,s),'tools remain outside the composer border');
 check(e.width>=s.width-44&&e.right<=s.right-6,'editor does not span the upper row');
 check(e.bottom<=t.top+1,'editor overlaps the bottom actions');
 check(inside(e,s),'editor extends beyond the surface');
 check(toolbar.scrollWidth<=toolbar.clientWidth+1&&$('.main').scrollWidth<=$('.main').clientWidth+1,'composer horizontal overflow');
 const buttons=[...toolbar.querySelectorAll('.tool,.send-menu-toggle,.send')].filter(button=>button.getBoundingClientRect().width>0).map(button=>({button,r:button.getBoundingClientRect()}));
 check(buttons.length===(mode==='running'?5:mode==='waiting'?3:4),'mode controls missing');
 for(let i=0;i<buttons.length;i++) {
  check(inside(buttons[i].r,t),'control extends beyond toolbar');
  for(let j=i+1;j<buttons.length;j++)check(buttons[i].r.right<=buttons[j].r.left+1||buttons[j].r.right<=buttons[i].r.left+1,'toolbar controls overlap');
 }
 const model=$('.c-tools-start .tool .v');check(model&&getComputedStyle(model).display!=='none','model name becomes invisible');
 if(mode!=='waiting') {
  const send=$('.send'),r=send.getBoundingClientRect(),style=getComputedStyle(send);
  check(toolbar.contains(send)&&r.top>=e.bottom,'send button floats beside editor instead of inside footer');
  check(Math.abs(r.width-r.height)<1&&r.width>=36&&parseFloat(style.borderRadius)>=r.width/2,'send button is not a usable circle');
  check(s.right-r.right>=8&&s.right-r.right<=20,'send is not aligned to the surface right edge');
  check(r.bottom<=innerHeight+1,'send is below the viewport');
 } else {
  check(!$('.send'),'waiting state exposes a misleading send action');
  check(shell.contains($('.c-opts'))&&$$('.c-opts button').length===2,'approval options were lost');
 }
 check(editor.getAttribute('aria-describedby')==='composer-help','input help relation missing');
};
try {
 (await wait(()=>$('dialog.account'),'account')).close();
 for(const mode of ['done','running','waiting','draft']) {
  location.hash=mode==='draft'?'#/w/p/new':'#/w/p/s/'+mode;
  await wait(()=>$('[data-react-composer="true"] .composer-toolbar'),'React unified composer');
  if(mode==='draft')await wait(()=>$('.main-landing')&&!$('.c-status'),'draft mode');
  else await wait(()=>$('#turn-surface-'+mode),'target conversation');
  if(mode==='waiting')await wait(()=>$('.composer.waiting')&&$$('.c-opts button').length===2,'approval mode');
  if(mode==='running')await wait(()=>$('.send-menu-toggle'),'running send menu');
  if(mode==='done')await wait(()=>$('.answer')?.textContent.includes('Surface done.'),'completed answer');
  for(const theme of ['light','dark']) {document.documentElement.dataset.theme=theme;await frame();inspect(mode);}
  const input=$('#composer'),initialHeight=input.getBoundingClientRect().height;
  input.value=Array.from({length:32},(_,i)=>'Line '+i+': preserve a full-width editing area.').join('\n');input.dispatchEvent(new Event('input',{bubbles:true}));
  await frame();inspect(mode);
  check($('#composer')===input&&input.getBoundingClientRect().height>initialHeight,'textarea did not grow in place');
  check(input.getBoundingClientRect().height<=261&&input.scrollHeight>input.clientHeight,'long draft has no bounded scrolling editor');
  input.value='A short draft';input.dispatchEvent(new Event('input',{bubbles:true}));await frame();inspect(mode);
  check(input.getBoundingClientRect().height<200,'textarea failed to shrink after clearing long text');
  $('.usage-tool').click();const meter=await wait(()=>$('.meter-grid'),'usage popover');
  check(!$('.prompt-input__shell').contains(meter),'popover is trapped inside the clipped surface');
  check(meter.getBoundingClientRect().width>120&&meter.getBoundingClientRect().right<=innerWidth+1,'usage detail no longer readable');
  input.click();await frame();
  if(mode==='running') {
   $('.send-menu-toggle').click();const menu=await wait(()=>$('.send-menu'),'running actions menu');
   check(menu.querySelectorAll('[data-action]').length===3&&!$('.prompt-input__shell').contains(menu),'running actions were lost or clipped');
   input.click();
  }
 }
 check(writes===0,'geometry interactions accidentally submitted or approved a task');
}catch(error){out.errors.push(String(error.stack||error));}
await original('/__result',{method:'POST',body:JSON.stringify(out)});
`
