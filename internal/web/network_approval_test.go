package web

import "testing"

func TestNetworkApprovalStaysBoundToRunningTask(t *testing.T) {
	_, result := runStreamProbe(t, networkApprovalProbe, []streamEvent{{"task_accepted", params{}}, {"round_started", params{"round_seq": 1}}, {"node_started", params{"round_seq": 1, "node_id": "verify", "role": "verify"}}})
	if len(result.Errors) > 0 {
		t.Fatalf("network approval: %v", result.Errors)
	}
	for _, key := range []string{"hostsVisible", "publicVisible", "approvedSameTask", "rejectedSameTask", "noAutoGrant"} {
		if result.Steps[key] != true {
			t.Errorf("%s: %v", key, result.Steps)
		}
	}
}

const networkApprovalProbe = `
const out={errors:[],steps:{}};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const requests=[],replies=[];const original=fetch.bind(window);
window.fetch=(input,init)=>{const path=typeof input==='string'?input:input.url;requests.push(path);if(path.endsWith('/reply')){replies.push({path,body:JSON.parse(init.body)});return Promise.resolve(new Response('{}',{status:200,headers:{'Content-Type':'application/json'}}));}return original(input,init);};
const $=s=>document.querySelector(s);const wait=async(fn,label)=>{for(let i=0;i<350;i++){const value=fn();if(value)return value;await new Promise(r=>setTimeout(r,25));}throw Error(label);};
const check=(ok,message)=>{if(!ok)throw Error(message);};
const push=(name,value)=>original('/__push',{method:'POST',body:JSON.stringify({name,value})});
try{
 await wait(()=>$('#turn-t1'),'running task');
 await push('human_input_required',{question_id:'network-hosts-1',kind:'tool_approval',question_code:'network_permission_hosts',text:'RAW irreversible fallback',params:{hosts:['docs.example.com','api.example.com'],ports:['443'],minutes:15,reason:'核对最新接口文档'},options:['approve','reject']});
 const card=await wait(()=>$('[data-network-task="t1"]'),'structured hosts approval');
 for(const value of ['修一下测试','docs.example.com','api.example.com','443','15','核对最新接口文档'])check(card.textContent.includes(value),'missing consent field '+value);
 check(!$('.c-ask').textContent.includes('RAW')&&!$('.c-ask').textContent.includes('network_permission_'),'approval code not localized');
 check(replies.length===0,'approval sent automatically');out.steps.hostsVisible=true;
 $('[data-network-approval="approve"]').click();await wait(()=>replies.length===1&&!$('[data-network-task]'),'approval handled');
 out.steps.approvedSameTask=replies[0].path==='/api/projects/p1/sessions/s1/tasks/t1/reply'&&replies[0].body.question_id==='network-hosts-1'&&replies[0].body.option==='approve';
 await push('tool_status',{tool:'web_fetch',target:'https://docs.example.com',state:'succeeded',round_seq:1});
 check(!!$('#turn-t1'),'approval replaced original task');
 await push('human_input_required',{question_id:'network-public-2',kind:'tool_approval',question_code:'network_permission_public',text:'RAW irreversible fallback',params:{hosts:[],ports:['80','443'],minutes:30,reason:'核对公开来源'},options:['approve','reject']});
 const publicCard=await wait(()=>$('[data-network-task="t1"]'),'public approval');
 check(publicCard.textContent.includes('所有公网')&&publicCard.textContent.includes('30')&&publicCard.textContent.includes('80')&&publicCard.textContent.includes('核对公开来源'),'public scope fields missing');out.steps.publicVisible=true;
 $('[data-network-approval="reject"]').click();await wait(()=>replies.length===2,'rejected');
 out.steps.rejectedSameTask=replies[1].path===replies[0].path&&replies[1].body.question_id==='network-public-2'&&replies[1].body.option==='reject';
 out.steps.noAutoGrant=!requests.some(path=>/network.grant|network-grant|network_grant/.test(path));
}catch(error){out.errors.push(String(error.stack||error));}
await original('/__result',{method:'POST',body:JSON.stringify(out)});
`
