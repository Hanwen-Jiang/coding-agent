package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Exercise the production bundle, its layout, and the actual SSE-to-React bridge.
// A large batch must remain readable without a second vertical scroll viewport.
func TestCompactTimelineKeepsToolDetailsAndStateAcrossLiveUpdates(t *testing.T) {
	const base = "/api/projects/p/sessions/s"
	command := `python -c "print('` + strings.Repeat("a-long-command-argument-", 24) + `')" --report test-summary.json`
	for _, scenario := range []struct{ language, viewport string }{{"en", "1440,900"}, {"zh", "390,744"}} {
		t.Run(scenario.language+scenario.viewport, func(t *testing.T) {
			task := params{"id": "compact", "status": "TASK_STATUS_RUNNING", "message": "Inspect the repository and verify the change.", "created_at": "2026-11-09T08:00:00Z"}
			events := []params{snapshotEvent(1, "compact", "task_accepted", params{})}
			for step := 1; step <= 10; step++ {
				tool, target, state := "repo_read", fmt.Sprintf("src/module-%d.ts", step), "succeeded"
				if step == 1 {
					tool, target = "shell_run", command
				}
				if step == 10 {
					tool, target, state = "shell_run", "npm test", "failed"
				}
				value := params{"round_seq": 1, "node_id": "inspect", "step": step, "tool": tool, "target": target, "state": "running"}
				if step == 1 {
					value["progress_message"] = "I will inspect the entry points and run the existing tests."
				}
				events = append(events, snapshotEvent(len(events)+1, "compact", "tool_status", value))
				result := params{"round_seq": 1, "node_id": "inspect", "step": step, "tool": tool, "target": target, "state": state, "duration_ms": 125}
				if state == "failed" {
					result["error"] = "Expected 42, received 41"
				}
				events = append(events, snapshotEvent(len(events)+1, "compact", "tool_status", result))
			}
			updates := make(chan params, 4)
			extra := map[string]any{
				"/api/projects": []params{{"id": "p", "name": "Workspace", "source": "empty"}}, "/api/sessions": []params{{"id": "s", "project_id": "p", "title": "Compact timeline"}}, "/api/environments": []any{}, "/api/personal-config": params{"config": params{}},
				base + "/tasks": []params{task}, base + "/tasks/compact": params{"task": task, "runtime": params{}}, base + "/tasks/compact/artifacts": params{"artifacts": []any{}}, base + "/env": params{}, base + "/changes": params{"files": []any{}},
				base + "/snapshot": params{"tasks": []params{{"task_id": "compact", "status": "running", "last_event_id": len(events), "source": "events", "events": events}}},
				"GET " + base + "/tasks/compact/events": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Cache-Control", "no-cache")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					for {
						select {
						case event := <-updates:
							data, _ := json.Marshal(event)
							fmt.Fprintf(w, "event: tool_status\nid: %v\ndata: %s\n\n", event["event_id"], data)
							w.(http.Flusher).Flush()
						case <-r.Context().Done():
							return
						}
					}
				}),
				"POST /__advance": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var value params
					if json.NewDecoder(r.Body).Decode(&value) != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					id := len(events) + 1
					if value["state"] == "succeeded" {
						id++
					}
					updates <- snapshotEvent(id, "compact", "tool_status", value)
					w.WriteHeader(http.StatusNoContent)
				}),
			}
			args, _ := json.Marshal(params{"command": command})
			raw := runAccountProbe(t, scenario.language, "const C="+string(args)+";\n"+compactTimelineProbe, params{}, extra, scenario.viewport)
			var result struct {
				Errors []string `json:"errors"`
			}
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("compact timeline: %v %s", err, raw)
			}
		})
	}
}

const compactTimelineProbe = `
const out={errors:[]};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
addEventListener('securitypolicyviolation',e=>out.errors.push('CSP: '+e.violatedDirective+' '+e.blockedURI));
const policy=document.createElement('meta');policy.httpEquiv='Content-Security-Policy';policy.content="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'";document.head.append(policy);
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const value=fn();if(value)return value;await new Promise(r=>setTimeout(r,25));}throw Error(label);};
const frame=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
const check=(value,label)=>{if(!value)throw Error(label);};
const push=state=>fetch('/__advance',{method:'POST',body:JSON.stringify({round_seq:1,node_id:'inspect',step:11,tool:'repo_read',target:'src/streamed.ts',state,duration_ms:250})});
try {
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';
 const host=await wait(()=>$('.conversation-island[data-pro-mounted="true"]'),'React conversation host');
 const flow=await wait(()=>host.querySelector('[data-pro-component="Conversation"]'),'React conversation');
 const group=await wait(()=>flow.querySelector('.progress-island'),'tool group');
 const head=group.querySelector('.worklog-head');
 check(head.getAttribute('aria-expanded')==='false','tools must start collapsed');
 check(group.querySelector('.execution-failures')?.textContent.includes('1'),'failed call is invisible while group collapsed');
 check(flow.querySelector('[data-public-progress]')?.textContent.includes('inspect the entry points'),'public progress missing before tools');
 head.click();await wait(()=>head.getAttribute('aria-expanded')==='true','group expanded');
 await wait(()=>group.querySelectorAll('[data-execution-step]').length===8,'large group must initially reveal eight tools');
 const list=group.querySelector('.worklog-steps');
 const first=group.querySelector('[data-execution-step]');
 const trigger=first.querySelector('.chat-tool__trigger');
 const target=first.querySelector('.execution-target');
 check(target?.textContent===C.command,'command preview changed its actual input');
 for(const theme of ['light','dark']) {
  document.documentElement.dataset.theme=theme;await frame();
  const preview=getComputedStyle(target),listStyle=getComputedStyle(list),rowStyle=getComputedStyle(group.querySelectorAll('[data-execution-step]')[1]);
  check(preview.whiteSpace==='nowrap'&&preview.textOverflow==='ellipsis','long command must remain a one-line preview');
  check(target.getBoundingClientRect().height<=parseFloat(preview.lineHeight)+2,'long command wrapped');
  check(trigger.getBoundingClientRect().height<=52,'tool trigger is an oversized card');
  check(!['auto','scroll'].includes(listStyle.overflowY)&&listStyle.maxHeight==='none','tool group creates nested vertical scrolling');
  check(parseFloat(listStyle.borderLeftWidth)>0&&parseFloat(listStyle.borderRightWidth)>0,'tool rows lack their shared border');
  check(parseFloat(rowStyle.borderLeftWidth)===0&&parseFloat(rowStyle.borderRightWidth)===0&&parseFloat(rowStyle.borderTopLeftRadius)===0,'individual tool cards remain');
  check($('.main').scrollWidth<=$('.main').clientWidth+1,'main horizontal overflow '+theme);
 }
 trigger.click();await wait(()=>trigger.getAttribute('aria-expanded')==='true','tool details expanded');
 await wait(()=>first.querySelector('.execution-code')?.textContent===C.command,'full shell input missing from detail');
 check($('.main').scrollWidth<=$('.main').clientWidth+1,'expanded command overflows the page');
 const more=group.querySelector('.execution-more');check(more?.textContent.includes('2'),'remaining tool count is inaccurate');
 more.click();await wait(()=>group.querySelectorAll('[data-execution-step]').length===10,'show more does not reveal real calls');
 const failed=group.querySelectorAll('[data-execution-step]')[9];failed.querySelector('.chat-tool__trigger').click();
 await wait(()=>failed.textContent.includes('Expected 42, received 41'),'actual failure detail missing');
 const input=$('#composer');input.value='Preserve this draft';input.dispatchEvent(new Event('input',{bubbles:true}));
 await push('running');await wait(()=>group.querySelectorAll('[data-execution-step]').length===11,'live new tool after show more');
 check($('.conversation-island')===host&&host.querySelector('[data-pro-component="Conversation"]')===flow,'stream update replaced the React host');
 check(group===flow.querySelector('.progress-island')&&first===group.querySelector('[data-execution-step]'),'stream update remounted tool group or rows');
 check(head.getAttribute('aria-expanded')==='true'&&trigger.getAttribute('aria-expanded')==='true','stream update lost expansion state');
 check($('#composer')===input&&input.value==='Preserve this draft','stream update lost composer state');
 check(group.querySelector('.execution-current')?.textContent.includes('src/streamed.ts'),'active tool not visible');
 head.click();await wait(()=>head.getAttribute('aria-expanded')==='false','group re-collapsed');
 await push('succeeded');await wait(()=>!group.querySelector('.execution-current'),'completion did not clear active tool');
 check(head.getAttribute('aria-expanded')==='false','completion reopened a collapsed group');
 head.click();await wait(()=>head.getAttribute('aria-expanded')==='true','group reopened');
 await wait(()=>group.querySelectorAll('[data-execution-step]').length===11,'show-more state lost on collapse');
 group.querySelector('.execution-more').click();await wait(()=>group.querySelectorAll('[data-execution-step]').length===8,'show less does not restore compact view');
 check(group.querySelector('.execution-failures')?.textContent.includes('1'),'late success cleared unrelated failure badge');
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`
