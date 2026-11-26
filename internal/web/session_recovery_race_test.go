package web

import "testing"

// Two session views in the same project are represented by distinct
// session URLs. Keep A's second snapshot waiting while B receives a genuine
// resnapshot_required frame from the test SSE server.
func TestRecoveryOfNewSessionIsIndependentOfPreviousSession(t *testing.T) {
	const probe = `
const out = { errors: [], steps: {} };
const waits = {s1: 0, s2: 0}, opens = {s1: 0, s2: 0};
const net = window.fetch.bind(window);
const NativeSource = window.EventSource;
const mapToFixture = u => u.replace('/sessions/s2/', '/sessions/s1/');
const routeOf = u => u.includes('/sessions/s2/') ? 's2' : 's1';
let releaseA;
let aReturned = false;
const slowA = new Promise(resolve => { releaseA = resolve; });
const snapshot = s => ({tasks: [
  {task_id: 't0', status: 'done', last_event_id: 3, events: [
    {event_id: 3, task_id: 't0', type:'TASK_EVENT_TYPE_TASK_FINISHED',
     payload:{value:{status:'TASK_STATUS_DONE',deliverable:{value:{answer:'answer-'+s}}}}}]},
  {task_id:'t1', status:'running', last_event_id: s === 's2' ? 9 : 5, events: []}
]});
const ok = data => new Response(JSON.stringify(data), {headers:{'Content-Type':'application/json'}});
window.fetch = async (input, init) => {
  if (typeof input !== 'string') return net(input, init);
  if (input === '/api/sessions') return ok(['s1','s2'].map(id => ({id,project_id:'p1',title:id})));
  if (input.endsWith('/snapshot')) {
    const s=routeOf(input);
    if (++waits[s] === 2 && s === 's1') { await slowA; aReturned = true; }
    return ok(snapshot(s));
  }
  return net(mapToFixture(input),init);
};
window.EventSource = class extends NativeSource {
  constructor(url, options) {
    super(mapToFixture(url), options);
    if (url.includes('/tasks/t1/events'))
      this.addEventListener('open', () => { opens[routeOf(url)]++; });
  }
};
const wait = async (f,label) => {
  const end=performance.now()+4000;
  while(!f() && performance.now()<end) await new Promise(r=>setTimeout(r,20));
  if(!f()) throw Error(label);
};
const expire = () => net('/__push',{method:'POST',body:JSON.stringify({name:'resnapshot_required',value:{}})});
try {
  await wait(()=>opens.s1===1,'first view did not subscribe');
  await expire();
  await wait(()=>waits.s1===2,'first recovery did not start');
  location.hash='#/w/p1/s/s2';
  await wait(()=>opens.s2===1,'second view did not subscribe');
  await expire();
  await wait(()=>opens.s2===2,'second view remained disconnected while first recovery was pending');
  releaseA();
  await wait(()=>aReturned,'delayed snapshot did not complete');
  await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
  await wait(()=>document.querySelector('.answer')?.textContent.includes('answer-s2'),'second view lost its history');
} catch(e) { out.errors.push(String(e)); }
finally { releaseA(); }
out.steps={waits,opens};
await net('/__result',{method:'POST',body:JSON.stringify(out)});
`
	_, result := runStreamProbe(t, probe, nil)
	t.Logf("two sessions: %v; errors=%v", result.Steps, result.Errors)
	if len(result.Errors) != 0 {
		t.Fatal(result.Errors)
	}
}
