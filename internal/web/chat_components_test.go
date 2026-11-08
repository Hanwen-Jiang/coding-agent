package web

import "testing"

func TestHeroUIAnswersAndAccurateContextInBrowser(t *testing.T) {
	_, result := runStreamProbe(t, heroUIAnswerProbe, []streamEvent{{"task_accepted", params{}}, {"round_started", params{"round_seq": 1}}, {"node_started", params{"round_seq": 1, "node_id": "answer", "role": "deliver"}}})
	if len(result.Errors) > 0 {
		t.Fatalf("Pro chat and context: %v", result.Errors)
	}
	for _, key := range []string{"proMarkdown", "codeCopy", "streamEnded", "unmounted", "contextRatio", "zeroHidden", "overflow"} {
		if result.Steps[key] != true {
			t.Errorf("%s: %v", key, result.Steps)
		}
	}
}

const heroUIAnswerProbe = `
const out={errors:[],steps:{}};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
addEventListener('securitypolicyviolation',e=>out.errors.push('CSP: '+e.violatedDirective+' '+e.blockedURI));
const meta=document.createElement('meta');meta.httpEquiv='Content-Security-Policy';meta.content="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'";document.head.append(meta);
const $=s=>document.querySelector(s),$$=s=>[...document.querySelectorAll(s)];
const wait=async(fn,label)=>{for(let i=0;i<400;i++){const v=fn();if(v)return v;await new Promise(r=>setTimeout(r,25));}throw Error(label+' '+document.body.textContent.slice(-1500));};
const check=(ok,message)=>{if(!ok)throw Error(message);};
const push=(name,value,delta=false)=>fetch('/__push',{method:'POST',body:JSON.stringify({name,value,delta})});
try {
 await wait(()=>$('#turn-t1'),'turn');
 const code='const answer = 42;';
 const text='# Verified answer\n\n**Ready** [source](https://example.com/docs)\n\n\x60\x60\x60js\n'+code+'\n\x60\x60\x60\n\n<script>window.__unsafe=true</script>\n\n[bad](javascript:alert(1))';
 await push('token_delta',{node_id:'answer',offset:0,text},true);
 await wait(()=>$('#turn-t1 [data-pro-component="ChatMessage"] [data-slot="markdown"]'),'real Pro components');
 await wait(()=>$('#turn-t1 [data-slot="code-block-code"] .shiki'),'actual Shiki highlighting');
 const host=$('#turn-t1 .pro-chat');
 check(host.dataset.proMounted==='true','React mount not confirmed');
 check(!window.__unsafe && !$('#turn-t1 .answer script') && !$$('#turn-t1 .answer a').some(a=>a.href.startsWith('javascript:')),'unsafe Markdown rendered');
 const link=$('#turn-t1 .answer a[href="https://example.com/docs"]');check(link.rel.includes('noopener')&&link.target==='_blank','external link isolation');
 out.steps.proMarkdown=true;
 let copied='';Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>{copied=text;}},configurable:true});
 $('#turn-t1 [data-slot="code-block-copy-button"]').click();await wait(()=>copied===code,'code copy');out.steps.codeCopy=true;
 await push('token_delta',{node_id:'answer',offset:0,text:'# Updated stream\n\n'+text},true);
 await wait(()=>host.textContent.includes('Updated stream'),'stream update');check(host===$('#turn-t1 .pro-chat'),'stream updates remounted the island');
 await push('context_usage',{window_tokens:1000000,effective_tokens:128000,total_tokens:13700,mask_at_tokens:51200,summarize_at_tokens:89600,categories:[{name:'system_prompt',tokens:1000},{name:'system_tools',tokens:3700},{name:'mcp_tools',tokens:0},{name:'memory',tokens:0},{name:'task_brief',tokens:0},{name:'messages',tokens:9000}]});
 await wait(()=>$('.ctx-num')&&$('.ctx-line').textContent.includes('1M'),'context usage');
 const bar=$('.ctx-bar'),free=$('.ctx-segment[data-cat="free"]');
 const ratio=free.getBoundingClientRect().width/bar.getBoundingClientRect().width;
 check(Math.abs(ratio-114300/128000)<.003,'free fraction is not budget based: '+ratio);
 check($('.ctx-row[data-cat="free"] .ctx-pct').textContent==='89.3%','legend uses a different denominator');
 for(const seg of $$('.ctx-segment'))check(getComputedStyle(seg).paddingLeft==='0px'&&getComputedStyle(seg).minWidth==='0px','segment has artificial width');
 out.steps.contextRatio=true;out.steps.zeroHidden=!$('.ctx-segment[data-cat="mcp_tools"]')&&!$('.ctx-segment[data-cat="memory"]');
 check($('.ctx-thresholds').textContent.includes('51.2k')&&$('.ctx-thresholds').textContent.includes('89.6k'),'real thresholds missing');
 await push('context_usage',{window_tokens:1000000,effective_tokens:128000,total_tokens:130000,categories:[{name:'messages',tokens:130000}]});
 await wait(()=>$('.ctx-overflow'),'over budget');check(!$('.ctx-thresholds'),'historical event must not invent thresholds');out.steps.overflow=true;
 await push('task_finished',{status:'TASK_STATUS_DONE',deliverable:{value:{answer:text+'\n\nFinal answer.'}}});
 await wait(()=>$('#turn-t1 [data-streaming="false"]')?.textContent.includes('Final answer.'),'final Pro answer');
 check(!$('#turn-t1 .answer.draft'),'streaming state not cleared');out.steps.streamEnded=true;
 location.hash='#/workspaces';await wait(()=>!host.isConnected&&!host.dataset.proMounted,'island unmount');out.steps.unmounted=true;
}catch(error){out.errors.push(String(error.stack||error));}
await fetch('/__result',{method:'POST',body:JSON.stringify(out)});
`
