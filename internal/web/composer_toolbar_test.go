package web

import (
	"encoding/json"
	"testing"
)

func TestComposerToolbarUsesOneRowAtDesktopAndMobile(t *testing.T) {
	const base = "/api/projects/p/sessions/s"
	task := params{"id": "history", "status": "TASK_STATUS_DONE", "message": "Existing report", "cost_usd": 1234567.89, "usage": params{"input_tokens": 123456789, "output_tokens": 987654321}}
	extra := map[string]any{
		"/api/projects": []params{{"id": "p", "name": "Workspace", "source": "empty"}}, "/api/sessions": []params{{"id": "s", "project_id": "p", "title": "History"}}, "/api/environments": []any{}, "/api/personal-config": params{"config": params{}},
		base + "/tasks": []params{task}, base + "/tasks/history": params{"task": task, "runtime": params{}}, base + "/tasks/history/artifacts": params{"artifacts": []any{}}, base + "/env": params{}, base + "/changes": params{"files": []any{}},
		base + "/snapshot": params{"tasks": []params{{"task_id": "history", "status": "done", "last_event_id": 1, "source": "events", "events": []params{snapshotEvent(1, "history", "task_finished", params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": "Existing report."}}})}}}},
	}
	for _, scenario := range []struct{ language, viewport string }{{"zh", "1440,900"}, {"en", "390,744"}, {"zh", "390,744"}} {
		t.Run(scenario.language+scenario.viewport, func(t *testing.T) {
			var result struct {
				Errors []string `json:"errors"`
			}
			raw := runAccountProbe(t, scenario.language, composerToolbarProbe, params{}, extra, scenario.viewport)
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("toolbar: %v %s", err, raw)
			}
		})
	}
}

const composerToolbarProbe = `
const out={errors:[]};addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<350;i++){const value=fn();if(value)return value;await new Promise(r=>setTimeout(r,25));}throw Error(label);};
const check=(ok,message)=>{if(!ok)throw Error(message);};
const original=fetch.bind(window);let taskWrites=0;
window.fetch=(url,opts)=>{if(opts?.method==='POST'&&/\/tasks$/.test(String(url)))taskWrites++;return original(url,opts);};
const inspect=()=>{
 const toolbar=$('.c-tools'),buttons=$$('.c-tools .tool'),r=toolbar.getBoundingClientRect();
 check(!$('.composer-footer')&&!$('.c-tools .c-hint'),'shortcut footer still visible');
 check(buttons.length===3&&$$('.c-tools-start .tool').length===2,'toolbar grouping');
 check(r.height===32,'toolbar must be one 32px row: '+r.height);
 check(buttons.every(button=>Math.abs(button.getBoundingClientRect().height-32)<1&&Math.abs(button.getBoundingClientRect().top-r.top)<1),'controls must align on one row');
 check(toolbar.scrollWidth<=toolbar.clientWidth,'toolbar overflows');
 const usage=$('.usage-tool'),u=usage.getBoundingClientRect(),style=getComputedStyle(usage);
 check(u.width>=64&&parseFloat(style.paddingLeft)>=12&&parseFloat(style.paddingRight)>=12,'usage hit area/padding too small');
 check(r.right-u.right>=3&&r.right-u.right<=13,'usage edge padding is not balanced');
 const footer=$('.composer-toolbar'),shell=$('.prompt-input__shell'),send=$('.send').getBoundingClientRect();
 check(shell.contains(footer)&&footer.contains(toolbar),'toolbar must share the editor surface');
 check(footer.contains($('.send'))&&send.left>=u.right&&send.top>=$('#composer').getBoundingClientRect().bottom,'send action must align after the tools below the editor');
 check(getComputedStyle($('.c-tools-start .tool .v')).display!=='none','model name hidden');
 check($('#composer').title.includes('Enter')&&$('#composer').getAttribute('aria-describedby')==='composer-help','shortcuts not discoverable');
};
try{
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';
 await wait(()=>$('.usage-tool')?.title.includes('1234567.89'),'full usage data');
 await wait(()=>$('[data-react-composer="true"] .prompt-input__shell'),'React composer surface');
 inspect();check($('.usage-tool').textContent==='$1.2M','large fee must be compact');
 $('.usage-tool').click();await wait(()=>$('.meter-grid'),'usage details');check($('.meter-grid').textContent.includes('1234567.89'),'exact fee lost from details');
 $('#composer').click();location.hash='#/w/p/new';
 await wait(()=>$('.usage-tool')?.textContent==='$0','read-only draft');await wait(()=>$('[data-react-composer="true"] .prompt-input__shell'),'React draft surface');inspect();
 check(taskWrites===0,'layout test must not submit a task');
}catch(error){out.errors.push(String(error.stack||error));}
await original('/__result',{method:'POST',body:JSON.stringify(out)});
`
