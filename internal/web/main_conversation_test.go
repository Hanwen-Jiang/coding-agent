package web

import (
	"encoding/json"
	"testing"
)

func TestMainConversationUsesRealComponentsWithoutLosingTaskControls(t *testing.T) {
	const base = "/api/projects/p/sessions/s"
	task := params{"id": "answer", "status": "TASK_STATUS_DONE", "message": "Review the repository and attach the test summary.", "created_at": "2026-11-09T08:00:00Z"}
	events := []params{
		snapshotEvent(1, "answer", "task_accepted", params{}),
		snapshotEvent(2, "answer", "plan_updated", params{"steps": []params{{"id": "read", "title": "Read repository", "status": "done", "round": 1}}, "current_step": "read"}),
		snapshotEvent(3, "answer", "tool_status", params{"round_seq": 1, "step": 1, "node_id": "read", "tool": "repo_read", "state": "succeeded", "target": "README.md"}),
		snapshotEvent(4, "answer", "tool_status", params{"round_seq": 1, "step": 2, "node_id": "test", "tool": "shell_run", "state": "succeeded", "target": "go test ./..."}),
		snapshotEvent(5, "answer", "task_finished", params{"status": "TASK_STATUS_DONE", "rounds": 1, "deliverable": params{"value": params{"answer": "## Test summary\n\nRead **README.md** and ran the existing tests.\n\n```go\nfunc Example() { println(42) }\n```\n\nThe exported report is attached."}}}),
	}
	extra := map[string]any{
		"/api/projects": []params{{"id": "p", "name": "Workspace", "source": "empty"}}, "/api/sessions": []params{{"id": "s", "project_id": "p", "title": "Review"}}, "/api/environments": []any{}, "/api/personal-config": params{"config": params{}},
		base + "/tasks": []params{task}, base + "/tasks/answer": params{"task": task, "runtime": params{}}, base + "/tasks/answer/artifacts": params{"artifacts": []params{{"name": "test-summary.csv", "size_bytes": 2048, "url": "/download/test-summary.csv", "mime_type": "text/csv"}}}, base + "/env": params{}, base + "/changes": params{"files": []any{}},
		base + "/snapshot": params{"tasks": []params{{"task_id": "answer", "status": "done", "last_event_id": 5, "source": "events", "events": events}}},
	}
	for _, scenario := range []struct{ language, viewport string }{{"zh", "1440,900"}, {"en", "900,600"}, {"zh", "390,744"}} {
		t.Run(scenario.language+scenario.viewport, func(t *testing.T) {
			var result struct {
				Errors []string `json:"errors"`
			}
			raw := runAccountProbe(t, scenario.language, mainConversationProbe, params{}, extra, scenario.viewport)
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("main conversation: %v %s", err, raw)
			}
		})
	}
}

const mainConversationProbe = `
const out={errors:[]};addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const v=fn();if(v)return v;await new Promise(r=>setTimeout(r,25));}throw Error(label);};
const check=(ok,label)=>{if(!ok)throw Error(label);};
try{
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';
 await wait(()=>$('[data-pro-component="PromptInput"]'),'PromptInput');
 await wait(()=>$('[data-pro-component="ChatMessage"] [data-slot="markdown"]'),'ChatMessage Markdown');
 await wait(()=>$('[data-pro-component="ChatAttachment"]'),'ChatAttachment');
 const trace=await wait(()=>$('[data-pro-component="ChainOfThought"]'),'ChainOfThought');
 const trigger=trace.querySelector('.worklog-head');check(trigger.getAttribute('aria-expanded')==='false','execution must start compact');
 trigger.click();await wait(()=>trace.querySelector('.worklog-head').getAttribute('aria-expanded')==='true','trace expansion');
 const tool=await wait(()=>trace.querySelector('.chat-tool__trigger'),'ChatTool');tool.click();
 await wait(()=>trace.textContent.includes('README.md'),'actual tool detail');
 check(trace.querySelector('.plan')?.textContent.includes('Read repository'),'actual plan missing');
 const attachment=$('.delivered-attachments a');check(attachment?.getAttribute('download')==='test-summary.csv'&&attachment.href.endsWith('/download/test-summary.csv'),'download binding lost');
 const input=$('#composer');input.focus();input.value='Keep this draft';input.dispatchEvent(new Event('input',{bubbles:true}));
 trigger.click();await wait(()=>trace.querySelector('.worklog-head').getAttribute('aria-expanded')==='false','trace collapse');
 check($('#composer')===input&&input.value==='Keep this draft','trace rendering replaced composer state');
 for(const theme of ['light','dark']){
  document.documentElement.dataset.theme=theme;
  const bar=$('.topbar').getBoundingClientRect();check(bar.height===44,'topbar height changed');
  check($('.main').scrollWidth<=$('.main').clientWidth,'main horizontal overflow '+theme);
  check($('.thread').getBoundingClientRect().width<=800,'conversation column too wide');
  check($('.dock').getBoundingClientRect().bottom<=innerHeight+1,'composer bottom clipped');
  const shell=$('.pro-prompt .prompt-input__shell').getBoundingClientRect(),editor=$('#composer').getBoundingClientRect(),send=$('.c-right .send').getBoundingClientRect();
  check(editor.width>=shell.width-44,'textarea shrunk inside PromptInput: '+editor.width+'/'+shell.width+' '+JSON.stringify([...function*(x){for(;x&&x!==document.body;x=x.parentElement)yield x;}($('#composer'))].map(x=>({class:x.className,width:x.getBoundingClientRect().width,display:getComputedStyle(x).display,flex:getComputedStyle(x).flex,min:getComputedStyle(x).minWidth,cssWidth:getComputedStyle(x).width}))));
  check($('.c-row').getBoundingClientRect().height<=80,'Pro textarea reserved unused toolbar space');
  check(shell.right-send.right>=4&&shell.right-send.right<=20,'send not aligned to shell edge');
  check($('.prompt-input__shell').contains($('.composer-toolbar'))&&send.top>=editor.bottom,'footer actions must share the input surface below the editor');
  check($('.c-tools').getBoundingClientRect().height===32,'composer toolbar wraps');
  check($$('.c-tools .tool').length===3,'task/model/usage controls missing');
 }
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

func TestNewConversationAndSimpleAnswersStayFocused(t *testing.T) {
	const base = "/api/projects/p/sessions/s"
	task := params{"id": "answer", "status": "TASK_STATUS_DONE", "message": "Can this task use the internet?", "created_at": "2026-11-09T08:00:00Z"}
	events := []params{
		snapshotEvent(1, "answer", "task_accepted", params{}),
		snapshotEvent(2, "answer", "round_started", params{"round_seq": 1}),
		snapshotEvent(3, "answer", "stage_started", params{"goal": "Reply to this message", "triage_level": "fast", "nodes": []string{"answer"}}),
		snapshotEvent(4, "answer", "node_started", params{"node_id": "answer", "role": "deliver"}),
		snapshotEvent(5, "answer", "node_finished", params{"node_id": "answer", "role": "deliver", "status": "succeeded"}),
		snapshotEvent(6, "answer", "verdict", params{"verdict": "done", "reason": "Simple reply uses rule review"}),
		snapshotEvent(7, "answer", "task_finished", params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": "Current task networking is authorized."}}}),
	}
	extra := map[string]any{
		"/api/projects": []params{{"id": "p", "name": "Workspace", "source": "empty"}}, "/api/sessions": []params{{"id": "s", "project_id": "p", "title": "Question"}}, "/api/environments": []any{}, "/api/personal-config": params{"config": params{}},
		base + "/settings": params{"sandbox": "write_reversible", "network_request": nil}, base + "/tasks": []params{task}, base + "/tasks/answer": params{"task": task, "runtime": params{}}, base + "/tasks/answer/artifacts": params{"artifacts": []any{}}, base + "/env": params{}, base + "/repos": params{"repos": []any{}},
		base + "/changes":  params{"files": []params{{"path": "old-change.py", "status": "modified"}}},
		base + "/snapshot": params{"tasks": []params{{"task_id": "answer", "status": "done", "last_event_id": 7, "source": "events", "events": events}}},
	}
	editTask := params{"id": "edited", "status": "TASK_STATUS_DONE", "message": "Fix the stock test.", "created_at": "2026-11-09T09:00:00Z"}
	extra["/api/sessions"] = []params{{"id": "s", "project_id": "p", "title": "Question"}, {"id": "edit", "project_id": "p", "title": "Code change"}}
	editBase := "/api/projects/p/sessions/edit"
	extra[editBase+"/settings"] = params{"sandbox": "write_reversible", "network_request": nil}
	extra[editBase+"/tasks"] = []params{editTask}
	extra[editBase+"/tasks/edited"] = params{"task": editTask, "runtime": params{}}
	extra[editBase+"/tasks/edited/artifacts"] = params{"artifacts": []any{}}
	extra[editBase+"/changes"] = params{"files": []params{{"path": "stock.py", "status": "modified"}}}
	extra[editBase+"/env"] = params{}
	extra[editBase+"/snapshot"] = params{"tasks": []params{{"task_id": "edited", "status": "done", "last_event_id": 2, "source": "events", "events": []params{
		snapshotEvent(1, "edited", "tool_status", params{"tool": "repo_edit", "state": "succeeded", "target": "stock.py"}),
		snapshotEvent(2, "edited", "task_finished", params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": "Updated stock.py and verified the change."}}}),
	}}}}
	for _, size := range []string{"1440,900", "900,600", "390,744"} {
		t.Run(size, func(t *testing.T) {
			var result struct {
				Errors []string `json:"errors"`
			}
			raw := runAccountProbe(t, "en", focusedConversationProbe, params{}, extra, size)
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("focused conversation: %v %s", err, raw)
			}
		})
	}
}

const focusedConversationProbe = `
const out={errors:[]};addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const v=fn();if(v)return v;await new Promise(r=>setTimeout(r,20));}throw Error(label);};
const check=(v,m)=>{if(!v)throw Error(m);};
const frame=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
try{
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/new';
 await wait(()=>$('.conversation-start [data-pro-component="PromptInput"]'),'integrated draft composer');
 for(const theme of ['light','dark']){
  document.documentElement.dataset.theme=theme;await frame();
  const title=$('.conversation-start h2').getBoundingClientRect(),shell=$('.prompt-input__shell').getBoundingClientRect(),suggestions=$('.start-suggestions').getBoundingClientRect(),tools=$('.c-tools').getBoundingClientRect();
  check(shell.top>=title.bottom&&shell.top-title.bottom<=32,'input detached from start heading');
  check(suggestions.top>=shell.bottom&&suggestions.top-shell.bottom<=24,'suggestions detached from input');
  check(shell.bottom<=innerHeight-60,'draft input pushed to viewport footer');
  check(!$('.hero-empty .suggestions')&&$$('.start-suggestion').length===3,'old large cards remain');
  check($('.main').scrollWidth<=$('.main').clientWidth,'draft horizontal overflow');
  check($('#composer').getBoundingClientRect().width>=shell.width-44,'draft editor narrow');
  check(shell.right-$('.send').getBoundingClientRect().right>=4&&shell.right-$('.send').getBoundingClientRect().right<=20,'draft send edge');
 }
 $('.start-suggestion').click();check($('#composer').value.includes('Read through'),'suggestion no longer fills actual composer');check(document.activeElement===$('#composer'),'suggestion lost input focus');
 location.hash='#/w/p/s/s';await wait(()=>$('.answer')?.textContent.includes('networking is authorized'),'actual answer');await frame();
 check(!$('.main-landing')&&$('.dock').parentElement.classList.contains('main'),'conversation input not docked');
 check(!$('.progress-island'),'fast answer shows internal pipeline steps');
 check($$('.run-diagnostics').length===1&&!$('.run-diagnostics').open,'diagnostics must be a single collapsed entry');
 check(!$$('.next-step').length,'stale workspace changes produce question CTAs');
 const input=$('#composer');input.value='Keep focused draft';input.dispatchEvent(new Event('input',{bubbles:true}));input.focus();
 $('.run-diagnostics > summary').click();check($('.run-diagnostics').open,'run details inaccessible');
 check($('.diagnostic-events').textContent.includes('Triage: fast')&&$('.diagnostic-events').textContent.includes('answer'),'diagnostic pipeline data lost');
 check($$('.execution-raw').length===1,'raw data repeated on individual events');
 $('.execution-raw > summary').click();check($('.execution-raw pre').textContent.includes('task_accepted'),'raw event payload lost');
 check($('#composer')===input&&input.value==='Keep focused draft','details replaced input node');
 check($('.dock').getBoundingClientRect().bottom<=innerHeight+1&&$('.c-tools').getBoundingClientRect().height===32,'conversation dock geometry');
 location.hash='#/w/p/s/edit';await wait(()=>$('.answer')?.textContent.includes('Updated stock.py'),'real edit answer');await wait(()=>$$('.next-step').length===2,'actual edit actions retained');
 check($('.next-steps').textContent.includes('Review changes')&&$('.next-steps').textContent.includes('Commit'),'missing actual current-task diff actions');
 location.hash='#/w/p/s/s';await wait(()=>$('.answer')?.textContent.includes('networking is authorized'),'question again');await wait(()=>!$$('.next-step').length,'prior edited task leaked action suggestions');
}catch(e){out.errors.push(String(e.stack||e));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`
