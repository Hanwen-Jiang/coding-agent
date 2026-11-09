package web

import "testing"

func TestPublicProgressInterleavesToolsAndSurvivesReload(t *testing.T) {
	_, result := runStreamProbe(t, conversationFlowProbe, []streamEvent{{"task_accepted", params{}}, {"round_started", params{"round_seq": 1}}})
	if len(result.Errors) != 0 {
		t.Fatalf("conversation flow: %v", result.Errors)
	}
	for _, key := range []string{"interleaved", "independentGroups", "draftCleared", "reloadStable", "rawOnce"} {
		if result.Steps[key] != true {
			t.Errorf("%s: %v", key, result.Steps)
		}
	}
}

const conversationFlowProbe = `
const out={errors:[],steps:{}};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const value=fn();if(value)return value;await sleep(25);}throw Error(label);};
const check=(value,label)=>{if(!value)throw Error(label);};
const saved=JSON.parse(sessionStorage.getItem('flow-reload')||'null');
const nativeFetch=window.fetch.bind(window),events=[];
if(saved)window.fetch=(url,init)=>String(url).endsWith('/snapshot')?Promise.resolve(new Response(JSON.stringify({tasks:[{task_id:'t1',status:'done',last_event_id:9999,source:'events',events:saved.events}]}),{headers:{'Content-Type':'application/json'}})):nativeFetch(url,init);
const push=async(name,value,delta=false)=>{
 if(!delta)events.push({event_id:1000+events.length,task_id:'t1',type:'TASK_EVENT_TYPE_'+name.toUpperCase(),emitted_at:'2026-11-09T08:00:00Z',payload:{value}});
 return nativeFetch('/__push',{method:'POST',body:JSON.stringify({name,value,delta})});
};
const call=(step,state,extra={})=>({round_seq:1,node_id:'work',step,state,tool:'repo_read',target:'README.md',...extra});
const signature=()=>$$('#turn-t1 .conversation-flow > div').map(el=>el.matches('.progress-note-island')?['note',el.textContent]:['tools',el.querySelector('.execution-summary')?.textContent]);
try{
 await wait(()=>$('#turn-t1'),'turn');
 if(!saved){
  await push('stage_started',{round_seq:1,triage_level:'full',goal:'先公开一句简短进展，本轮暂不修改。',nodes:[]});
  await push('token_delta',{node_id:'work',offset:0,text:'先读取项目入口与现有测试。'},true);
  await wait(()=>$('#turn-t1 .answer.draft'),'streamed public text');
  await push('tool_status',call(1,'running',{progress_message:'先读取项目入口与现有测试。'}));
  await wait(()=>$('#turn-t1 [data-public-progress]'),'public progress');
  check(!$('#turn-t1 .conversation-flow').textContent.includes('先公开一句'),'planner instruction exposed as assistant prose');
  check(!$('#turn-t1 .answer.draft'),'tool-loop text duplicated as final answer draft');out.steps.draftCleared=true;
  await push('tool_status',call(1,'running',{call_index:1,tool:'repo_grep',target:'parseInput'}));
  await push('tool_status',call(2,'running',{tool:'shell_run',target:'go test ./internal/parser',progress_message:'调用入口已经确认，接下来运行解析器测试。'}));
  // Completion can arrive after another progress note; it must update the original group.
  await push('tool_status',call(1,'succeeded'));
  await push('tool_status',call(1,'succeeded',{call_index:1,tool:'repo_grep',target:'parseInput'}));
  await push('tool_status',call(2,'failed',{tool:'shell_run',target:'go test ./internal/parser',error:'one assertion failed'}));
  await wait(()=>$$('#turn-t1 [data-public-progress]').length===2&&$$('#turn-t1 .worklog-head').length===2,'two groups');
  check(JSON.stringify(signature().map(p=>p[0]))===JSON.stringify(['note','tools','note','tools']),'progress/tool order');out.steps.interleaved=true;
  const heads=$$('#turn-t1 .worklog-head');check(heads.every(el=>el.getAttribute('aria-expanded')==='false'),'default compact groups');
  heads[0].click();await wait(()=>$$('#turn-t1 .worklog-head')[0].getAttribute('aria-expanded')==='true','first expanded');
  check($$('#turn-t1 .worklog-head')[1].getAttribute('aria-expanded')==='false','second group opened with first');
  check($$('#turn-t1 .progress-island')[0].querySelectorAll('[data-execution-step]').length===2,'late results moved groups');
  heads[0].click();out.steps.independentGroups=true;
  await push('task_finished',{status:'TASK_STATUS_DONE',deliverable:{value:{answer:'解析器测试存在一处断言失败。'}}});
  await wait(()=>$('#turn-t1 .answer')?.textContent.includes('断言失败'),'separate final answer');
  check(!$('#turn-t1 .conversation-flow').textContent.includes('断言失败'),'final answer repeated inside flow');
  out.steps.rawOnce=$$('#turn-t1 .run-diagnostics').length===1&&$$('#turn-t1 .execution-raw').length===1;
  sessionStorage.setItem('flow-reload',JSON.stringify({events,signature:signature(),steps:out.steps}));
  location.reload();
 }else{
  await wait(()=>$$('#turn-t1 [data-public-progress]').length===2&&$('#turn-t1 .answer')?.textContent.includes('断言失败'),'snapshot reload');
  check(JSON.stringify(signature())===JSON.stringify(saved.signature),'snapshot and live order differ');
  check($$('#turn-t1 .run-diagnostics').length===1,'duplicate raw entry after reload');
  out.steps={...saved.steps,reloadStable:true};sessionStorage.removeItem('flow-reload');
  await nativeFetch('/__result',{method:'POST',body:JSON.stringify(out)});
 }
}catch(error){out.errors.push(String(error.stack||error));await nativeFetch('/__result',{method:'POST',body:JSON.stringify(out)});}
`
