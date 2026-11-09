package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConversationMotionSurvivesRapidTogglesAndRespectsReducedMotion(t *testing.T) {
	const base = "/api/projects/p/sessions/s"
	browser := findBrowser()
	if browser == "" {
		t.Skip("找不到浏览器（CODING_TEST_BROWSER 可以指定）")
	}
	for _, reduced := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduced=%t", reduced), func(t *testing.T) {
			if reduced {
				if runtime.GOOS == "windows" {
					t.Skip("此浏览器参数包装器使用 Unix shell")
				}
				wrapper := filepath.Join(t.TempDir(), "chrome-reduced-motion")
				script := "#!/bin/sh\nexec '" + strings.ReplaceAll(browser, "'", "'\"'\"'") + "' --force-prefers-reduced-motion \"$@\"\n"
				if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CODING_TEST_BROWSER", wrapper)
			}
			task := params{"id": "motion", "status": "TASK_STATUS_RUNNING", "message": "Read and verify the repository.", "created_at": "2026-11-09T08:00:00Z"}
			events := []params{
				snapshotEvent(1, "motion", "task_accepted", params{}),
				snapshotEvent(2, "motion", "round_started", params{"round_seq": 1}),
				snapshotEvent(3, "motion", "tool_status", params{"round_seq": 1, "node_id": "read", "step": 1, "tool": "repo_read", "target": "README.md", "state": "running", "progress_message": "I will inspect the repository and verify the existing tests."}),
				snapshotEvent(4, "motion", "tool_status", params{"round_seq": 1, "node_id": "read", "step": 1, "tool": "repo_read", "target": "README.md", "state": "succeeded", "duration_ms": 125}),
			}
			updates := make(chan params, 8)
			extra := map[string]any{
				"/api/projects": []params{{"id": "p", "name": "Workspace", "source": "empty"}}, "/api/sessions": []params{{"id": "s", "project_id": "p", "title": "Motion"}}, "/api/environments": []any{}, "/api/personal-config": params{"config": params{}},
				base + "/tasks": []params{task}, base + "/tasks/motion": params{"task": task, "runtime": params{}}, base + "/tasks/motion/artifacts": params{"artifacts": []any{}}, base + "/env": params{}, base + "/changes": params{"files": []any{}},
				base + "/snapshot": params{"tasks": []params{{"task_id": "motion", "status": "running", "last_event_id": 4, "source": "events", "events": events}}},
				"GET " + base + "/tasks/motion/events": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					for {
						select {
						case event := <-updates:
							data, _ := json.Marshal(event)
							name := strings.ToLower(strings.TrimPrefix(event["type"].(string), "TASK_EVENT_TYPE_"))
							fmt.Fprintf(w, "event: %s\nid: %v\ndata: %s\n\n", name, event["event_id"], data)
							w.(http.Flusher).Flush()
						case <-r.Context().Done():
							return
						}
					}
				}),
				"POST /__advance": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var update struct {
						ID    int    `json:"id"`
						Type  string `json:"type"`
						Value params `json:"value"`
					}
					if json.NewDecoder(r.Body).Decode(&update) != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					updates <- snapshotEvent(update.ID, "motion", update.Type, update.Value)
					w.WriteHeader(http.StatusNoContent)
				}),
			}
			args, _ := json.Marshal(params{"reduced": reduced})
			raw := runAccountProbe(t, "en", "const C="+string(args)+";\n"+conversationMotionProbe, params{}, extra, "1440,900")
			var result struct {
				Errors []string `json:"errors"`
			}
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("conversation motion: %v %s", err, raw)
			}
		})
	}
}

const conversationMotionProbe = `
const out={errors:[]};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
addEventListener('securitypolicyviolation',e=>out.errors.push('CSP: '+e.violatedDirective+' '+e.blockedURI));
const policy=document.createElement('meta');policy.httpEquiv='Content-Security-Policy';policy.content="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'";document.head.append(policy);
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const value=fn();if(value)return value;await sleep(20);}throw Error(label);};
const check=(value,label)=>{if(!value)throw Error(label);};
const finiteAnimations=el=>el.getAnimations({subtree:true}).filter(animation=>animation.effect.getTiming().iterations!==Infinity);
const settle=async el=>{await sleep(30);await Promise.all(finiteAnimations(el).map(animation=>animation.finished.catch(()=>{})));await sleep(30);};
const push=(id,type,value)=>fetch('/__advance',{method:'POST',body:JSON.stringify({id,type,value})});
const tool=(step,state)=>({round_seq:1,node_id:'read',step,tool:'repo_read',target:'src/streamed-'+step+'.ts',state,duration_ms:200});
const animationCounts=new Map();
addEventListener('animationstart',event=>animationCounts.set(event.target,(animationCounts.get(event.target)||0)+1),true);
try {
 check(matchMedia('(prefers-reduced-motion: reduce)').matches===C.reduced,'browser reduced-motion preference not configured');
 (await wait(()=>$('dialog.account'),'account')).close();location.hash='#/w/p/s/s';
 const note=await wait(()=>$('.progress-note-island'),'public progress');
 const group=await wait(()=>$('.progress-island'),'tool group'),head=group.querySelector('.worklog-head');
 const panel=await wait(()=>$('details.run-diagnostics'),'run inspector');
 const summary=panel.querySelector(':scope > summary');
 for(let i=0;i<3;i++){summary.click();await sleep(35);}
 await settle(panel);
 check(panel.open,'native details rapid open-close-open ended closed');
 const body=panel.querySelector('.run-diagnostics-body');
 check(panel.getBoundingClientRect().height>=summary.getBoundingClientRect().height+body.getBoundingClientRect().height-2,'native details content stayed clipped after rapid reversal');
 check(panel.querySelector('.diagnostic-metrics').getBoundingClientRect().height>0,'native details content inaccessible');
 for(let i=0;i<3;i++){head.click();await sleep(35);}
 await settle(group);
 const content=group.querySelector('[data-slot="chain-of-thought-content"]');
 check(head.getAttribute('aria-expanded')==='true'&&!content.hasAttribute('hidden'),'Pro disclosure rapid reversal ended hidden');
 check(content.clientHeight>=content.scrollHeight-2,'Pro group remained clipped after expansion');
 const initialTool=group.querySelector('.chat-tool__trigger');initialTool.click();await settle(group);
 const toolContent=group.querySelector('[data-slot="chat-tool-content"]');
 check(initialTool.getAttribute('aria-expanded')==='true'&&toolContent.clientHeight>=toolContent.scrollHeight-2,'Pro tool details remained clipped');
 await sleep(350);
 const noteStarts=animationCounts.get(note)||0;
 let laterNoteAnimations=0;
 const animateNote=note.animate.bind(note);note.animate=(...args)=>{laterNoteAnimations++;return animateNote(...args);};
 await push(5,'tool_status',tool(2,'running'));
 const row=await wait(()=>[...group.querySelectorAll('[data-execution-step]')].find(row=>row.textContent.includes('src/streamed-2.ts')),'streamed tool');
 const status=row.querySelector('.execution-status');
 let pulseRequests=0;
 const animate=status.animate.bind(status);status.animate=(...args)=>{pulseRequests++;return animate(...args);};
 await settle(group);
 await push(6,'tool_status',tool(2,'succeeded'));
 await wait(()=>status.dataset.settled==='true','completion feedback was not triggered');
 let completionAnimation,completionTime;
 if(C.reduced)check(status.getAnimations().length===0,'reduced motion still animates completion');
 else {
  completionAnimation=await wait(()=>status.getAnimations().find(animation=>animation.playState==='running'),'completion did not animate');
  completionTime=completionAnimation.currentTime;
 }
 // Another call may start before the short completion feedback has finished.
 await push(7,'tool_status',tool(3,'running'));
 await wait(()=>group.textContent.includes('src/streamed-3.ts'),'concurrent stream update during completion');
 if(completionAnimation) {
  check(completionAnimation.playState==='finished'||status.getAnimations().includes(completionAnimation),'concurrent SSE replaced the completion animation');
  check(completionAnimation.currentTime!==null&&completionAnimation.currentTime>=completionTime,'concurrent SSE restarted completion animation time');
 }
 await wait(()=>status.dataset.settled==='false','completion feedback did not settle');
 const starts=pulseRequests;
 check(starts===(C.reduced?0:1),'completion feedback did not run exactly once: '+starts);
 await push(6,'tool_status',tool(2,'succeeded'));
 await push(8,'tool_status',tool(3,'succeeded'));
 await wait(()=>!group.querySelector('.execution-current'),'unrelated tool completed');
 await sleep(350);
 check(pulseRequests===starts&&status.dataset.settled==='false'&&status.getAnimations().length===0,'later SSE update replayed completion animation');
 check($('.progress-note-island')===note,'later SSE update replaced old progress DOM');
 check((animationCounts.get(note)||0)===noteStarts,'later SSE update replayed old progress animation: '+noteStarts+' -> '+(animationCounts.get(note)||0));
 check(laterNoteAnimations===0&&note.getAnimations().length===0,'later SSE update replayed old progress entrance');
 check(content.clientHeight>=content.scrollHeight-2,'new tools clipped by stale animated height');
 if(C.reduced) {
  const active=$('.main').getAnimations({subtree:true}).filter(animation=>animation.playState==='running'||animation.playState==='pending');
  check(active.length===0,'reduced motion leaves decorative animations running');
  summary.click();await wait(()=>!panel.open,'reduced motion inspector collapse');summary.click();await wait(()=>panel.open,'reduced motion inspector reopen');
  check(panel.querySelector('.diagnostic-metrics').getBoundingClientRect().height>0,'reduced motion disables content');
 }
 check($('.main').scrollWidth<=$('.main').clientWidth+1,'animation introduced horizontal overflow');
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`
