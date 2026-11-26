package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestConversationPolishUXAndScrollBehavior(t *testing.T) {
	const baseS1 = "/api/projects/p/sessions/s1"
	const baseS2 = "/api/projects/p/sessions/s2"
	longCmd := `python -c "print('` + strings.Repeat("polish-scroll-check-", 12) + `')" --json`
	s1Task := params{"id": "t1", "status": "TASK_STATUS_RUNNING", "message": "Inspect workspace and stream updates.", "created_at": "2026-11-27T08:00:00Z"}
	s1Events := []params{snapshotEvent(1, "t1", "task_accepted", params{})}
	for step := 1; step <= 10; step++ {
		tool, target, state := "repo_read", fmt.Sprintf("pkg/file-%d.go", step), "succeeded"
		if step == 1 {
			tool, target = "shell_run", longCmd
		}
		if step == 10 {
			tool, target, state = "shell_run", "npm test", "failed"
		}
		runEv := params{"round_seq": 1, "node_id": "work", "step": step, "tool": tool, "target": target, "state": "running"}
		if step == 1 {
			runEv["progress_message"] = "Progress note with inline code:\n\n```sh\ngo test ./...\n```"
		}
		s1Events = append(s1Events, snapshotEvent(len(s1Events)+1, "t1", "tool_status", runEv))
		resEv := params{"round_seq": 1, "node_id": "work", "step": step, "tool": tool, "target": target, "state": state, "duration_ms": 140}
		if step == 1 {
			resEv["output_preview"] = "ok  github.com/agentruntime/pkg 0.14s"
		}
		if step == 2 {
			tool, target = "repo_edit", "pkg/file-2.go"
			runEv["tool"], runEv["target"] = tool, target
			resEv["tool"], resEv["target"] = tool, target
			resEv["diff"] = "@@ -1 +1 @@\n-oldLine()\n+newLine()"
		}
		if state == "failed" {
			resEv["error"] = "test assertion failed on line 42"
		}
		s1Events = append(s1Events, snapshotEvent(len(s1Events)+1, "t1", "tool_status", resEv))
	}

	s2Task := params{"id": "t2", "status": "TASK_STATUS_DONE", "message": "Summarize architecture highlights.", "created_at": "2026-11-27T08:00:00Z", "cost_usd": 0.0123}
	s2Events := []params{
		snapshotEvent(1, "t2", "task_accepted", params{}),
		{
			"event_id":   2,
			"task_id":    "t2",
			"type":       "TASK_EVENT_TYPE_TASK_FINISHED",
			"emitted_at": "2026-11-27T08:00:15Z",
			"payload": params{"value": params{
				"status":      "TASK_STATUS_DONE",
				"rounds":      2,
				"deliverable": params{"value": params{"answer": "## Highlights\n\nAll **unit tests** passed.\n\n![logo](/assets/favicon.svg)"}},
			}},
		},
	}

	for _, scenario := range []struct{ language, viewport string }{{"zh", "1440,900"}, {"en", "390,744"}} {
		t.Run(scenario.language+scenario.viewport, func(t *testing.T) {
			updates := make(chan params, 8)
			var feedbackMu sync.Mutex
			var feedbackVotes []string
			extra := map[string]any{
				"/api/projects":        []params{{"id": "p", "name": "Workspace", "source": "empty"}},
				"/api/sessions":        []params{{"id": "s1", "project_id": "p", "title": "Streaming session"}, {"id": "s2", "project_id": "p", "title": "Completed session"}},
				"/api/environments":    []any{},
				"/api/personal-config": params{"config": params{}},

				baseS1 + "/tasks":              []params{s1Task},
				baseS1 + "/tasks/t1":           params{"task": s1Task, "runtime": params{}},
				baseS1 + "/tasks/t1/artifacts": params{"artifacts": []any{}},
				baseS1 + "/env":                params{},
				baseS1 + "/changes":            params{"files": []any{}},
				baseS1 + "/tree":               []params{{"name": "README.md", "dir": false, "size": 120}, {"name": "pkg", "dir": true}},
				baseS1 + "/tree?path=pkg":      []params{{"name": "file-2.go", "dir": false, "size": 42}},
				baseS1 + "/file":               params{"content": "package pkg\nfunc newLine() {}\n", "size": 28},
				baseS1 + "/snapshot":           params{"tasks": []params{{"task_id": "t1", "status": "running", "last_event_id": len(s1Events), "source": "events", "events": s1Events}}},

				baseS2 + "/tasks":              []params{s2Task},
				baseS2 + "/tasks/t2":           params{"task": s2Task, "runtime": params{}},
				baseS2 + "/tasks/t2/artifacts": params{"artifacts": []any{}},
				baseS2 + "/env":                params{},
				baseS2 + "/changes":            params{"files": []any{}},
				baseS2 + "/tree":               []params{{"name": "README.md", "dir": false, "size": 120}},
				baseS2 + "/snapshot":           params{"tasks": []params{{"task_id": "t2", "status": "done", "last_event_id": 2, "source": "events", "events": s2Events}}},

				"GET " + baseS1 + "/tasks/t1/events": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Cache-Control", "no-cache")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					for {
						select {
						case ev := <-updates:
							data, _ := json.Marshal(ev)
							evType := strings.ToLower(strings.TrimPrefix(ev["type"].(string), "TASK_EVENT_TYPE_"))
							fmt.Fprintf(w, "event: %s\nid: %v\ndata: %s\n\n", evType, ev["event_id"], data)
							w.(http.Flusher).Flush()
						case <-r.Context().Done():
							return
						}
					}
				}),
				"POST /__delta": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req struct {
						Text string `json:"text"`
					}
					if json.NewDecoder(r.Body).Decode(&req) != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					updates <- params{
						"event_id":   len(s1Events) + 1,
						"task_id":    "t1",
						"type":       "token_delta",
						"emitted_at": "2026-11-27T08:00:10Z",
						"payload":    params{"value": params{"node_id": "answer", "offset": 0, "text": req.Text}},
					}
					w.WriteHeader(http.StatusNoContent)
				}),
				"POST " + baseS2 + "/tasks/t2/feedback": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req struct {
						Vote string `json:"vote"`
					}
					if json.NewDecoder(r.Body).Decode(&req) != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					feedbackMu.Lock()
					feedbackVotes = append(feedbackVotes, req.Vote)
					feedbackMu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(params{"ok": true, "vote": req.Vote})
				}),
			}
			args, _ := json.Marshal(params{"cmd": longCmd})
			raw := runAccountProbe(t, scenario.language, "const C="+string(args)+";\n"+conversationPolishProbe, params{}, extra, scenario.viewport)
			var result struct {
				Errors []string `json:"errors"`
			}
			if err := json.Unmarshal(raw, &result); err != nil || len(result.Errors) > 0 {
				t.Fatalf("conversation polish: %v %s", err, raw)
			}
			feedbackMu.Lock()
			gotVotes := append([]string(nil), feedbackVotes...)
			feedbackMu.Unlock()
			if len(gotVotes) != 1 || gotVotes[0] != "up" {
				t.Fatalf("expected [up] persisted to /tasks/t2/feedback, got %v", gotVotes)
			}
		})
	}
}

const conversationPolishProbe = `
const out={errors:[]};
addEventListener('error',e=>out.errors.push(e.message));
addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
addEventListener('securitypolicyviolation',e=>out.errors.push('CSP: '+e.violatedDirective+' '+e.blockedURI));
const policy=document.createElement('meta');
policy.httpEquiv='Content-Security-Policy';
policy.content="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'";
document.head.append(policy);
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const v=fn();if(v)return v;await sleep(25);}throw Error(label);};
const frame=()=>new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
const check=(ok,label)=>{if(!ok)throw Error(label);};
const nativeFetch=fetch.bind(window);
let writes=0;
window.fetch=(url,opts)=>{if(opts?.method==='POST'&&/\/(tasks|guidance|reply)$/.test(String(url)))writes++;return nativeFetch(url,opts);};
const pushDelta=text=>nativeFetch('/__delta',{method:'POST',body:JSON.stringify({text})});
let copied='';
Object.defineProperty(navigator,'clipboard',{value:{writeText:async t=>{copied=t;}},configurable:true});
try{
 (await wait(()=>$('dialog.account'),'account')).close();
 location.hash='#/w/p/s/s1';
 const flow=await wait(()=>$('[data-pro-component="Conversation"]'),'conversation');
 check(!!flow.querySelector('[data-public-progress] [data-slot="code-block-code"]'),'progress note code block missing');
 const head=flow.querySelector('.worklog-head');
 head.click();
 await wait(()=>head.getAttribute('aria-expanded')==='true','group expanded');
 const jumpFailed=await wait(()=>flow.querySelector('.flow-jump-failed'),'jump to failure button missing for hidden row 10 error');
 const firstStep=flow.querySelector('[data-execution-step]');
 firstStep.querySelector('.chat-tool__trigger').click();
 const execCode=await wait(()=>firstStep.querySelector('.execution-code'),'execution code');
 check(execCode.textContent===C.cmd,'execution code text mutated');
 const outBox=await wait(()=>firstStep.querySelector('.flow-tool-output'),'tool output preview missing');
 check(outBox.textContent.includes('0.14s'),'tool output text missing');
 const cmdCopy=await wait(()=>firstStep.querySelector('.execution-code-copy'),'execution copy button missing');
 cmdCopy.click();
 await wait(()=>copied===C.cmd&&cmdCopy.dataset.copied==='true','command inline copy failed');
 const secondStep=flow.querySelectorAll('[data-execution-step]')[1];
 secondStep.querySelector('.chat-tool__trigger').click();
 const diffBox=await wait(()=>secondStep.querySelector('.flow-diff-preview'),'tool diff preview missing');
 check(diffBox.textContent.includes('+newLine()'),'tool diff line missing');
 const openFileBtn=await wait(()=>secondStep.querySelector('.execution-open-file'),'tool open file button missing');
 jumpFailed.click();
 await wait(()=>flow.querySelectorAll('[data-execution-step]').length===10,'jump to failed did not reveal all steps');
 // Generate long streaming draft so the viewport actually overflows and can scroll up.
 const longMd=Array.from({length:48},(_,i)=>'Paragraph '+i+': streaming output line to test scroll auto-follow lock without hijacking user reading position.').join('\n\n');
 await pushDelta(longMd);
 const draftHost=await wait(()=>$('#turn-t1 .answer.draft .pro-chat[data-pro-mounted="true"]'),'streaming pro host');
 check(getComputedStyle(draftHost,'::after').content==='none','duplicate outer streaming caret still active');
 const scroller=$('.main .scroll');
 await wait(()=>scroller.scrollHeight>scroller.clientHeight+200,'scroller did not overflow');
 scroller.scrollTop=0;
 scroller.dispatchEvent(new Event('scroll'));
 await frame();
 await pushDelta(longMd+'\n\nFinal incoming delta paragraph.');
 await wait(()=>draftHost.textContent.includes('Final incoming delta paragraph.'),'second delta not rendered');
 await frame();
 check(scroller.scrollTop<60,'SSE update hijacked user scrollTop back to bottom: '+scroller.scrollTop);
 const unread=await wait(()=>$('.scroll-to-bottom[data-unread="true"]'),'unread indicator missing while scrolled up');
 unread.click();
 await wait(()=>scroller.scrollHeight-scroller.scrollTop-scroller.clientHeight<=80,'scroll-to-bottom did not reach bottom');
 await wait(()=>!$('.scroll-to-bottom[data-unread="true"]'),'unread state not cleared at bottom');
 // Per-session draft preservation
 const input=$('#composer');
 input.value='Draft for session one';
 input.dispatchEvent(new Event('input',{bubbles:true}));
 location.hash='#/w/p/s/s2';
 const answer2=await wait(()=>$('#turn-t2 .answer .pro-chat[data-pro-mounted="true"]'),'s2 answer');
 check($('#composer').value==='','s1 draft leaked into s2');
 check($$('#turn-t2 .result-foot .chip').length===4,'status + rounds + elapsed duration + cost chips missing');
 const promptCopy=$('#turn-t2 .msg-copy-prompt');
 promptCopy.click();
 await wait(()=>copied==='Summarize architecture highlights.'&&promptCopy.dataset.copied==='true','user prompt copy failed');
 const ansCopy=$('#turn-t2 .result-copy');
 ansCopy.click();
 await wait(()=>copied.includes('All **unit tests** passed.')&&ansCopy.dataset.copied==='true','assistant inline copy failed');
 // Wave 2: feedback thumbs toggle, image lightbox zoom + Esc close, / and @ autocomplete
 const thumbUp=$('#turn-t2 .result-feedback[data-feedback="up"]');
 thumbUp.click();
 await wait(()=>thumbUp.getAttribute('aria-pressed')==='true','feedback thumbs up not pressed');
 const zoomBtn=await wait(()=>answer2.querySelector('.md-img-zoom'),'image zoom button missing');
 zoomBtn.click();
 await wait(()=>$('.image-lightbox-backdrop'),'image lightbox did not open');
 window.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
 await wait(()=>!$('.image-lightbox-backdrop'),'image lightbox did not close on Escape');
 $('#turn-t2 .msg-reuse-prompt').click();
 check($('#composer').value.includes('Summarize architecture highlights.'),'reuse prompt did not fill composer');
 location.hash='#/w/p/s/s1';
 await wait(()=>$('#turn-t1')&&$('#composer').value==='Draft for session one','s1 draft was lost across navigation');
 const cInput=$('#composer');
 cInput.value='/';
 cInput.setSelectionRange(1,1);
 cInput.dispatchEvent(new Event('input',{bubbles:true}));
 const slashMenu=await wait(()=>$('.composer-ac-menu'),'slash autocomplete menu did not open');
 check(slashMenu.textContent.includes('/files'),'/files missing from slash menu');
 cInput.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
 await wait(()=>!$('.composer-ac-menu'),'slash menu did not close on Escape');
 cInput.value='Check @REA';
 cInput.setSelectionRange(10,10);
 cInput.dispatchEvent(new Event('input',{bubbles:true}));
 const fileMenu=await wait(()=>$('.composer-ac-menu'),'file @ autocomplete menu did not open');
 await wait(()=>fileMenu.textContent.includes('@README.md'),'@README.md missing from file autocomplete');
 fileMenu.querySelector('.composer-ac-item').dispatchEvent(new MouseEvent('mousedown',{bubbles:true}));
 await wait(()=>cInput.value.includes('@README.md'),'selecting @README.md did not insert file mention');
 for(const theme of ['light','dark']){
  document.documentElement.dataset.theme=theme;
  await frame();
  check($('.main').scrollWidth<=$('.main').clientWidth+1,'horizontal overflow in '+theme);
 }
 check(writes===0,'polish probe accidentally triggered a write request');
}catch(error){out.errors.push(String(error.stack||error));}
await nativeFetch('/__result',{method:'POST',body:JSON.stringify(out)});
`
