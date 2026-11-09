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
  check(editor.width>=shell.width-110,'textarea shrunk inside PromptInput: '+editor.width+'/'+shell.width+' '+JSON.stringify([...function*(x){for(;x&&x!==document.body;x=x.parentElement)yield x;}($('#composer'))].map(x=>({class:x.className,width:x.getBoundingClientRect().width,display:getComputedStyle(x).display,flex:getComputedStyle(x).flex,min:getComputedStyle(x).minWidth,cssWidth:getComputedStyle(x).width}))));
  check(shell.right-send.right>=4&&shell.right-send.right<=20,'send not aligned to shell edge');
  check($('.c-tools').getBoundingClientRect().height===32,'composer toolbar wraps');
  check($$('.c-tools .tool').length===3,'task/model/usage controls missing');
 }
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`
