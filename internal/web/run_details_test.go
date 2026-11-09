package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestRunDetailsStayCompactAndPreserveDisclosureStateDuringStreaming(t *testing.T) {
	const base = "/api/projects/p/sessions/s"
	command := "npm test -- --reporter=" + strings.Repeat("very-long-reporter-name-", 24)
	for _, scenario := range []struct{ language, viewport string }{{"en", "1440,900"}, {"zh", "390,744"}} {
		t.Run(scenario.language+scenario.viewport, func(t *testing.T) {
			task := params{"id": "inspect", "status": "TASK_STATUS_RUNNING", "message": "Verify the failing test and inspect its execution.", "created_at": "2026-11-09T08:00:00Z"}
			events := []params{
				snapshotEvent(1, "inspect", "task_accepted", params{}),
				snapshotEvent(2, "inspect", "round_started", params{"round_seq": 1}),
				snapshotEvent(3, "inspect", "stage_started", params{"round_seq": 1, "goal": "Inspect the failing tests", "triage_level": "full", "nodes": []string{"read"}}),
				snapshotEvent(4, "inspect", "node_started", params{"round_seq": 1, "node_id": "read", "role": "execute"}),
				snapshotEvent(5, "inspect", "tool_status", params{"round_seq": 1, "node_id": "read", "step": 1, "tool": "repo_read", "target": "src/parser.ts", "state": "running"}),
				snapshotEvent(6, "inspect", "tool_status", params{"round_seq": 1, "node_id": "read", "step": 1, "tool": "repo_read", "target": "src/parser.ts", "state": "succeeded", "duration_ms": 125}),
				snapshotEvent(7, "inspect", "verdict", params{"round_seq": 1, "verdict": "continue", "reason": "Verify the parser with the test suite"}),
				snapshotEvent(8, "inspect", "round_started", params{"round_seq": 2}),
				snapshotEvent(9, "inspect", "tool_status", params{"round_seq": 2, "node_id": "verify", "step": 1, "tool": "shell_run", "target": command, "state": "running"}),
				snapshotEvent(10, "inspect", "tool_status", params{"round_seq": 2, "node_id": "verify", "step": 1, "tool": "shell_run", "target": command, "state": "failed", "duration_ms": 950, "error": "Expected 42, received 41"}),
			}
			updates := make(chan params, 4)
			extra := map[string]any{
				"/api/projects": []params{{"id": "p", "name": "Workspace", "source": "empty"}}, "/api/sessions": []params{{"id": "s", "project_id": "p", "title": "Run details"}}, "/api/environments": []any{}, "/api/personal-config": params{"config": params{}},
				base + "/tasks": []params{task}, base + "/tasks/inspect": params{"task": task, "runtime": params{}}, base + "/tasks/inspect/artifacts": params{"artifacts": []any{}}, base + "/env": params{}, base + "/changes": params{"files": []any{}},
				base + "/snapshot": params{"tasks": []params{{"task_id": "inspect", "status": "running", "last_event_id": 10, "source": "events", "events": events}}},
				"GET " + base + "/tasks/inspect/events": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
					id := 11
					if value["state"] == "succeeded" {
						id = 12
					}
					updates <- snapshotEvent(id, "inspect", "tool_status", value)
					w.WriteHeader(http.StatusNoContent)
				}),
			}
			args, _ := json.Marshal(params{"command": command})
			raw := runAccountProbe(t, scenario.language, "const C="+string(args)+";\n"+runDetailsProbe, params{}, extra, scenario.viewport)
			var result struct {
				Errors []string `json:"errors"`
			}
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("run details: %v %s", err, raw)
			}
		})
	}
}

const runDetailsProbe = `
const out={errors:[]};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
addEventListener('securitypolicyviolation',e=>out.errors.push('CSP: '+e.violatedDirective+' '+e.blockedURI));
const policy=document.createElement('meta');policy.httpEquiv='Content-Security-Policy';policy.content="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'";document.head.append(policy);
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const value=fn();if(value)return value;await new Promise(r=>setTimeout(r,25));}throw Error(label);};
const frame=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
const check=(value,label)=>{if(!value)throw Error(label);};
const metric=name=>{const node=$('.diagnostic-metric[data-metric="'+name+'"]');return Number(node?.dataset.value??node?.querySelector('.diagnostic-metric-value')?.textContent);};
const push=state=>fetch('/__advance',{method:'POST',body:JSON.stringify({round_seq:2,node_id:'verify',step:2,tool:'repo_read',target:'src/streamed.ts',state,duration_ms:250})});
try {
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';
 const panel=await wait(()=>$('details.run-diagnostics'),'run details');
 await wait(()=>panel.closest('[data-pro-mounted="true"]'),'React diagnostics host');
 const host=panel.closest('[data-pro-mounted="true"]');
 check(!panel.open,'run details must start collapsed');
 const summary=panel.querySelector(':scope > summary');summary.click();await wait(()=>panel.open,'run details expanded');
 await wait(()=>metric('events')===10,'event count includes actual source events');
 check(metric('rounds')===2,'round count does not reflect actual rounds');
 check(metric('calls')===2,'running/completed statuses double-count tool calls');
 const visibleEvents=()=>[...panel.querySelectorAll('.diagnostic-event')].filter(entry=>entry.getBoundingClientRect().height>0);
 check(visibleEvents().length===6,'inspector should initially show only six recent events');
 const more=panel.querySelector('.diagnostic-more');check(more,'older events must remain accessible');
 more.click();await wait(()=>visibleEvents().length===8,'show earlier events does not reveal the full history');
 const entries=[...panel.querySelectorAll('details.diagnostic-event')];
 check(entries.length>0&&entries.every(entry=>!entry.open),'event detail should be opt-in');
 const failed=entries.find(entry=>entry.textContent.includes('Expected 42, received 41'));
 check(failed,'failure explanation is missing');
 const failedSummary=failed.querySelector(':scope > summary');
 check(failedSummary.textContent.includes(C.command),'long command title is not represented');
 for(const theme of ['light','dark']) {
  document.documentElement.dataset.theme=theme;await frame();
  check(summary.getBoundingClientRect().height<=52,'run summary is an oversized card');
  check(failedSummary.getBoundingClientRect().height<=52,'long event title wraps into a tall card');
  check($('.main').scrollWidth<=$('.main').clientWidth+1,'diagnostics overflow the page '+theme);
  check(panel.scrollWidth<=panel.clientWidth+1,'long event overflows its inspector '+theme);
  const list=panel.querySelector('.diagnostic-events'),style=getComputedStyle(list);
  check(!['auto','scroll'].includes(style.overflowY)&&style.maxHeight==='none','diagnostics introduce nested vertical scrolling');
 }
 failedSummary.click();await wait(()=>failed.open,'failure detail expanded');
 await wait(()=>failed.innerText.includes('Expected 42, received 41'),'expanded error is readable after disclosure transition');
 const raw=panel.querySelector('details.execution-raw');
 check(raw&&!raw.open&&$$('.execution-raw').length===1,'raw data must have exactly one collapsed entry');
 raw.querySelector(':scope > summary').click();await wait(()=>raw.open,'raw event data expanded');
 const data=()=>JSON.parse(raw.querySelector('pre').textContent);
 check(data().length===10&&data().some(event=>event.value?.target===C.command),'raw data lost the complete command');
 check($('.main').scrollWidth<=$('.main').clientWidth+1,'raw JSON overflows the page');
 await push('running');await wait(()=>metric('events')===11&&data().length===11,'live event reflected in counters and raw data');
 check(metric('calls')===3&&metric('rounds')===2,'live call updates wrong counters');
 check($('details.run-diagnostics')===panel&&panel.closest('[data-pro-mounted="true"]')===host,'SSE remounted diagnostics');
 check(panel.open&&failed.isConnected&&failed.open&&raw.open,'SSE reset a user-opened disclosure');
 check(data().some(event=>event.value?.target==='src/streamed.ts'),'raw data did not update');
 summary.click();await wait(()=>!panel.open,'inspector collapsed');
 await push('succeeded');await wait(()=>metric('events')===12&&data().length===12,'completion reflected while collapsed');
 check(!panel.open,'completion reopened the inspector');
 check(metric('calls')===3,'completion double-counted the streamed tool');
 summary.click();await wait(()=>panel.open,'inspector reopened');
 check(failed.open&&raw.open,'parent collapse lost inner disclosure states');
 check(data().filter(event=>event.value?.target==='src/streamed.ts').length===2,'raw start and completion events not retained');
 check($$('.execution-raw').length===1&&$('.main').scrollWidth<=$('.main').clientWidth+1,'inspector duplicates raw entry or overflows after streaming');
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`
