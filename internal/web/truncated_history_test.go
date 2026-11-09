package web

import (
	"fmt"
	"testing"
	"time"
)

func TestTruncatedSnapshotBackfillsEveryProgressGroupAfterReload(t *testing.T) {
	events := make([]params, 0, 251)
	for step := 1; step <= 125; step++ {
		common := params{"round_seq": 1, "node_id": "work", "step": step, "tool": "repo_read", "target": fmt.Sprintf("file-%03d.go", step)}
		running := params{}
		for k, v := range common {
			running[k] = v
		}
		running["state"], running["progress_message"] = "running", fmt.Sprintf("公开进展 %03d", step)
		common["state"] = "succeeded"
		events = append(events, snapshotEvent(step*2-1, "t1", "tool_status", running), snapshotEvent(step*2, "t1", "tool_status", common))
	}
	events = append(events, snapshotEvent(251, "t1", "task_finished", params{"status": "TASK_STATUS_DONE", "deliverable": params{"value": params{"answer": "长任务最终答复"}}}))
	site, result := runStreamProbe(t, truncatedHistoryProbe, nil, func(s *streamSite) {
		s.snapshot = params{"tasks": []params{{"task_id": "t1", "status": "done", "last_event_id": 251, "events_truncated": true, "source": "events", "events": events[len(events)-50:]}}}
		s.sessionEvents = func(request map[string]any) any {
			cursor := request["cursors"].([]any)[0].(map[string]any)
			after := int(cursor["after_event_id"].(float64))
			if after == 0 {
				time.Sleep(500 * time.Millisecond) // Verify the snapshot paints before backfill.
			}
			end := min(after+60, len(events))
			start := max(0, after-1) // Page boundaries may replay an event.
			return params{"tasks": []params{{"task_id": "t1", "found": true, "events": events[start:end], "next_after_event_id": end, "more": end < len(events), "finished": true}}}
		}
	})
	if len(result.Errors) != 0 {
		t.Fatalf("truncated history: %v", result.Errors)
	}
	if result.Steps["snapshotFirst"] != true || result.Steps["completeAfterReload"] != true {
		t.Fatalf("history recovery: %v", result.Steps)
	}
	if got := len(site.seen("/events")); got != 10 {
		t.Errorf("two loads must each fetch all five pages, got %d", got)
	}
	if got := site.seen("/tasks/t1/events"); len(got) != 0 {
		t.Errorf("completed snapshot must not open duplicate SSE streams: %v", got)
	}
}

func TestHistoryBackfillKeepsLiveEventsAndDraft(t *testing.T) {
	events := []params{snapshotEvent(1, "t1", "tool_status", params{"round_seq": 1, "node_id": "work", "step": 1, "tool": "repo_read", "target": "old.go", "state": "running", "progress_message": "较早的公开进展"})}
	for i := 2; i <= 80; i++ {
		events = append(events, snapshotEvent(i, "t1", "tool_status", params{"round_seq": 1, "node_id": "work", "step": i, "tool": "repo_read", "target": "current.go", "state": "succeeded"}))
	}
	site, result := runStreamProbe(t, liveBackfillProbe, nil, func(s *streamSite) {
		s.seq = 80
		s.snapshot = params{"tasks": []params{{"task_id": "t1", "status": "running", "last_event_id": 80, "events_truncated": true, "source": "events", "events": events[30:]}}}
		s.sessionEvents = func(request map[string]any) any {
			time.Sleep(900 * time.Millisecond)
			return params{"tasks": []params{{"task_id": "t1", "found": true, "events": events, "next_after_event_id": 80, "more": false}}}
		}
	})
	if len(result.Errors) != 0 {
		t.Fatalf("live backfill: %v", result.Errors)
	}
	if result.Steps["preserved"] != true || len(site.seen("/tasks/t1/events")) != 1 {
		t.Fatalf("backfill must preserve one live stream and its draft: %v, %v", result.Steps, site.seen("/tasks/t1/events"))
	}
}

const liveBackfillProbe = `
const out={errors:[],steps:{}};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s);
const wait=async(fn,label)=>{for(let i=0;i<500;i++){if(fn())return;await new Promise(r=>setTimeout(r,20));}throw Error(label);};
const push=(name,value,delta=false)=>fetch('/__push',{method:'POST',body:JSON.stringify({name,value,delta})});
try{
 await wait(()=>$('#turn-t1 .worklog-head'),'snapshot first');
 await push('tool_status',{round_seq:1,node_id:'work',step:81,tool:'repo_read',target:'latest.go',state:'running',progress_message:'回填期间到达的新进展'});
 await push('token_delta',{node_id:'answer',offset:0,text:'正在生成的公开答复'},true);
 await wait(()=>$('#turn-t1 .answer.draft')?.textContent.includes('正在生成的公开答复'),'live draft');
 await wait(()=>$('#turn-t1 .conversation-flow')?.textContent.includes('较早的公开进展'),'older notes restored');
 if(!$('#turn-t1 .conversation-flow').textContent.includes('回填期间到达的新进展'))throw Error('backfill erased newer event');
 if(!$('#turn-t1 .answer.draft')?.textContent.includes('正在生成的公开答复'))throw Error('backfill erased current draft');
 out.steps.preserved=true;
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`

const truncatedHistoryProbe = `
const out={errors:[],steps:{}};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<500;i++){if(fn())return;await new Promise(r=>setTimeout(r,20));}throw Error(label);};
const check=(ok,label)=>{if(!ok)throw Error(label);};
try{
 await wait(()=>$('#turn-t1 .answer')?.textContent.includes('长任务最终答复'),'snapshot answer first');
 out.steps.snapshotFirst=!$('#turn-t1 .conversation-flow')?.textContent.includes('公开进展 001');
 await wait(()=>$$('#turn-t1 [data-public-progress]').length===125,'all 125 progress notes restored');
 check($$('#turn-t1 .worklog-head').length===125,'duplicate or missing tool groups');
 check($$('#turn-t1 [data-public-progress]')[0].textContent==='公开进展 001','first note missing');
 check($$('#turn-t1 [data-public-progress]')[124].textContent==='公开进展 125','last note missing');
 check($$('#turn-t1 .run-diagnostics').length===1,'duplicate diagnostics');
 if(!sessionStorage.getItem('long-history-reloaded')){
  sessionStorage.setItem('long-history-reloaded',JSON.stringify(out.steps));location.reload();
 }else{
  out.steps={...JSON.parse(sessionStorage.getItem('long-history-reloaded')),completeAfterReload:true};
  sessionStorage.removeItem('long-history-reloaded');await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
 }
}catch(error){out.errors.push(String(error.stack||error));await fetch('/__result',{method:'POST',body:JSON.stringify(out)});}
`
