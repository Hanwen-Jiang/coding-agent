package web

import "testing"

func TestReactComposerKeepsDraftAndCompositionDuringLiveUpdates(t *testing.T) {
	_, result := runStreamProbe(t, reactComposerProbe, []streamEvent{{"task_accepted", params{}}, {"round_started", params{"round_seq": 1}}})
	if len(result.Errors) > 0 {
		t.Fatalf("React composer: %v", result.Errors)
	}
}

const reactComposerProbe = `
const out={errors:[],steps:{}};
addEventListener('error',e=>out.errors.push(e.message));addEventListener('unhandledrejection',e=>out.errors.push(String(e.reason)));
const $=s=>document.querySelector(s);
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const wait=async(fn,label)=>{for(let i=0;i<300;i++){const value=fn();if(value)return value;await sleep(25);}throw Error(label);};
const check=(ok,label)=>{if(!ok)throw Error(label);};
const original=fetch.bind(window),writes=[];
window.fetch=(url,options)=>{if(options?.method==='POST'&&/\/guidance$/.test(String(url))){writes.push(JSON.parse(options.body));return Promise.resolve(new Response('{}',{headers:{'Content-Type':'application/json'}}));}return original(url,options);};
const push=(name,value)=>original('/__push',{method:'POST',body:JSON.stringify({name,value})});
try {
 await wait(()=>$('[data-react-composer="true"] .send-menu-toggle'),'React running composer');
 const input=$('#composer');
 check(input.matches('[data-slot="prompt-input-textarea"]'),'editor must be the real Pro TextArea');
 check($('.send').matches('[data-slot="prompt-input-send"]'),'send must be the real Pro Send');
 input.focus();input.value='保留我的草稿';input.dispatchEvent(new Event('input',{bubbles:true}));input.setSelectionRange(2,4);
 await wait(()=>!$('.send').disabled,'send enabled');
 const composing=new KeyboardEvent('keydown',{key:'Enter',isComposing:true,bubbles:true,cancelable:true});input.dispatchEvent(composing);
 const newline=new KeyboardEvent('keydown',{key:'Enter',shiftKey:true,bubbles:true,cancelable:true});input.dispatchEvent(newline);
 await sleep(50);check(writes.length===0&&!composing.defaultPrevented&&!newline.defaultPrevented,'IME/Shift+Enter submitted or swallowed');
 await push('tool_status',{round_seq:1,node_id:'read',step:1,tool:'repo_read',state:'succeeded',target:'README.md'});
 await sleep(150);check($('#composer')===input&&input.value==='保留我的草稿','SSE replaced editor or draft');
 check(input.selectionStart===2&&input.selectionEnd===4&&document.activeElement===input,'SSE lost selection/focus');
 input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));
 await wait(()=>writes.length===1,'Enter steering');check(writes[0].text==='保留我的草稿'&&writes[0].mode==='next_step','Enter semantics changed');
 await wait(()=>input.value===''&&$('.send').disabled,'draft cleared after send');
 input.value='中断后检查';input.dispatchEvent(new Event('input',{bubbles:true}));
 input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',ctrlKey:true,bubbles:true,cancelable:true}));
 await wait(()=>writes.length===2,'Ctrl Enter interrupt');check(writes[1].mode==='interrupt','interrupt semantics changed');
 input.value='按钮发送';input.dispatchEvent(new Event('input',{bubbles:true}));await wait(()=>!$('.send').disabled,'button enabled');$('.send').click();
 await wait(()=>writes.length===3,'button steering');await sleep(100);check(writes.length===3&&writes[2].text==='按钮发送','button sent duplicate or stale text');
} catch(error) {out.errors.push(String(error.stack||error));}
await original('/__result',{method:'POST',body:JSON.stringify(out)});
`
