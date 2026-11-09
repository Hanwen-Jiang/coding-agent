import test from 'node:test';
import assert from 'node:assert/strict';
import {conversationFlow, toolGroupCounts} from '../../internal/web/static/conversation-flow.js';

const event = (event_id,type,value) => ({event_id,type,value});
const tool = (id,step,state,extra={}) => event(id,'tool_status',{round_seq:1,node_id:'work',step,state,tool:'repo_read',...extra});

test('public notes interleave with their own tool groups; late results stay in place',()=>{
 const events=[tool(1,1,'running',{progress_message:'Read the existing implementation.'}),
  tool(2,2,'running'),tool(3,3,'running',{progress_message:'The parser handles empty values. Check its callers.',tool:'shell_run'}),
  tool(4,1,'succeeded'),tool(5,2,'failed'),tool(6,3,'succeeded',{tool:'shell_run'}),
  event(7,'task_finished',{deliverable:{answer:'Final answer'}})];
 const parts=conversationFlow(events);
 assert.deepEqual(parts.map(p=>p.kind),['progress','tools','progress','tools']);
 assert.deepEqual(parts[1].calls.map(c=>c.event.value.state),['succeeded','failed']);
 assert.equal(parts[3].calls[0].event.value.tool,'shell_run');
 assert.deepEqual(toolGroupCounts(parts[1].calls),{read:2,search:0,command:0,edit:0,other:0});
 assert.equal(JSON.stringify(parts).includes('Final answer'),false);
 // Duplicate delivery and snapshot order do not change either identities or results.
 assert.deepEqual(conversationFlow([...events].reverse().concat(events[0],events[2])),parts);
});

test('only explicit public fields become prose, and legacy tools still work',()=>{
 const parts=conversationFlow([
  event(1,'stage_started',{goal:'Reply to this message',triage_level:'fast'}),
  event(2,'stage_started',{goal:'Explore the task',goal_code:'explore_task'}),
  event(3,'thinking_delta',{text:'PRIVATE THINKING'}),
  event(4,'node_finished',{rationale:'PRIVATE RATIONALE'}),
  tool(5,1,'running',{rationale:'PRIVATE RATIONALE'}),tool(6,1,'succeeded'),
  event(7,'stage_started',{goal:'先公开一句简短进展，本轮暂不修改。',triage_level:'full'}),
  tool(8,1,'running',{round_seq:2,progress_message:'   '}),tool(9,1,'succeeded',{round_seq:2}),
 ]);
 assert.deepEqual(parts.map(p=>p.kind),['tools']);
 assert.equal(parts.filter(p=>p.kind==='progress').length,0);
 assert.equal(parts[0].calls.length,2);
 assert.equal(JSON.stringify(parts).includes('先公开一句'),false);
});

test('repeated running status cannot undo completion or duplicate a public note',()=>{
 const parts=conversationFlow([tool(1,1,'running',{call_index:0,progress_message:'Inspect the file.'}),tool(2,1,'succeeded',{call_index:0}),tool(3,1,'running',{call_index:0,progress_message:'Inspect the file.'})]);
 assert.equal(parts.length,2);
 assert.equal(parts[1].calls.length,1);
 assert.equal(parts[1].calls[0].event.value.state,'succeeded');
});

test('a batch preserves repeated tools and matches their results without swallowing calls',()=>{
 const parts=conversationFlow([
  tool(1,1,'running',{target:'a.go',progress_message:'Read the callers.'}),
  tool(2,1,'running',{target:'b.go'}),tool(3,1,'running',{target:'a.go'}),
  tool(4,1,'succeeded',{target:'b.go'}),tool(5,1,'succeeded',{target:'a.go'}),tool(6,1,'failed',{target:'a.go'}),
 ]);
 assert.equal(parts.length,2);
 assert.deepEqual(parts[1].calls.map(c=>[c.event.value.target,c.event.value.state]),[['a.go','succeeded'],['b.go','succeeded'],['a.go','failed']]);
 assert.equal(toolGroupCounts(parts[1].calls).read,3);
 const indexed=conversationFlow([
  tool(1,1,'running',{call_index:0,target:'a.go'}),tool(2,1,'running',{call_index:1,target:'a.go'}),
  tool(3,1,'failed',{call_index:1,target:'a.go'}),tool(4,1,'succeeded',{call_index:0,target:'a.go'}),
 ]);
 assert.deepEqual(indexed[0].calls.map(c=>c.event.value.state),['succeeded','failed']);
});

test('runtime batches omit the first call index and keep identities after out-of-order completion',()=>{
 const starts=[
  tool(1,1,'running',{target:'a.go',progress_message:'Inspect the entry points.'}),
  tool(2,1,'running',{call_index:1,target:'a.go'}),
  tool(3,2,'running',{target:'b.go',progress_message:'Verify the shared configuration.'}),
 ];
 const before=conversationFlow(starts);
 const events=[...starts,
  tool(4,1,'failed',{call_index:1,target:'a.go'}),
  tool(5,2,'succeeded',{target:'b.go'}),
  tool(6,1,'succeeded',{target:'a.go'}),
 ];
 const after=conversationFlow(events);
 assert.deepEqual(after.map(p=>[p.kind,p.id]),before.map(p=>[p.kind,p.id]));
 assert.deepEqual(after[1].calls.map(c=>c.id),before[1].calls.map(c=>c.id));
 assert.deepEqual(after[1].calls.map(c=>c.event.value.state),['succeeded','failed']);
 assert.equal(after[3].calls[0].event.value.state,'succeeded');
 // A snapshot plus overlapping SSE delivery must reconstruct the same groups.
 assert.deepEqual(conversationFlow([...events.slice(3),...events,...starts]),after);
});

test('identical public text in separate rounds remains separate chronological updates',()=>{
 const message='Run the regression tests.';
 const parts=conversationFlow([
  tool(1,1,'running',{progress_message:message}),tool(2,1,'succeeded'),
  event(3,'round_started',{round_seq:2}),
  tool(4,1,'running',{round_seq:2,progress_message:message}),tool(5,1,'succeeded',{round_seq:2}),
  event(6,'task_finished',{deliverable:{answer:'Final answer'}}),
 ]);
 assert.deepEqual(parts.map(p=>p.kind),['progress','tools','progress','tools']);
 assert.deepEqual(parts.filter(p=>p.kind==='progress').map(p=>p.message),[message,message]);
 assert.notEqual(parts[0].id,parts[2].id);
 assert.notEqual(parts[1].calls[0].id,parts[3].calls[0].id);
});

test('public note limits match the runtime Unicode code-point boundary',()=>{
 const message='a'.repeat(1999)+'😀';
 const parts=conversationFlow([tool(1,1,'running',{progress_message:message+'discarded'})]);
 assert.equal(parts[0].message,message);
 assert.equal([...parts[0].message].length,2000);
});
