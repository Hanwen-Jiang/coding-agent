import React, {useEffect,useMemo,useRef,useState} from 'react';
import {Button} from '@heroui/react';
import {ChainOfThought} from '@heroui-pro/react/chain-of-thought';
import {ChatTool} from '@heroui-pro/react/chat-tool';
import {ChatMessage} from '@heroui-pro/react/chat-message';
import {CodeBlock} from '@heroui-pro/react/code-block';
import {Markdown} from '@heroui-pro/react/markdown';
import {ChatLoader} from '@heroui-pro/react/chat-loader';
import type {Components} from 'react-markdown';
import {Icon} from './icon';

type Step = {id:string;label:string;target:string;detail:string;error:string;tool:string;state:'input-available'|'output-error'|'output-available';time:string;duration:string};
type Plan = {title:string;agentsTitle:string;steps:Array<{id:string;title:string;status:string;statusLabel:string;current:boolean}>;agents:Array<{id:string;name:string;state:string;statusLabel:string;label:string;last:string}>};
type Group = {kind:'tools';id:string;reveal?:number;title:string;current:string;running:boolean;steps:Step[];plan?:Plan};
type Note = {kind:'progress';id:string;message:string;source:string};
export type ConversationProps = {parts:Array<Group|Note>;labels:{more:string;less:string;command:string;target:string;failed:string;plan:string;copy?:string;copied?:string;copyFailed?:string;jumpFailed?:string}};

function publicHref(href?:string) {
 try {const url=new URL(href || '',location.href);return href&&['http:','https:','mailto:'].includes(url.protocol)?url.href:undefined;}catch{return undefined;}
}
function CopyInline({text,label,copiedLabel,failedLabel,className='btn ghost xs code-block__copy-button'}:{text:string;label?:string;copiedLabel?:string;failedLabel?:string;className?:string}) {
 const [state,setState]=useState<'ready'|'copied'|'failed'>('ready');
 const timer=useRef<ReturnType<typeof setTimeout>|null>(null);
 useEffect(()=>()=>{if(timer.current)clearTimeout(timer.current);},[]);
 const lbl=label||'Copy';
 return <button type="button" className={className} data-copied={state==='copied'?'true':undefined} aria-label={lbl} aria-live="polite" onClick={async()=>{
  try{await navigator.clipboard.writeText(text);setState('copied');}catch{setState('failed');}
  if(timer.current)clearTimeout(timer.current);timer.current=setTimeout(()=>setState('ready'),2000);
 }}><Icon name={state==='copied'?'check':'copy'} size="xs"/><span>{state==='copied'?(copiedLabel||lbl):state==='failed'?(failedLabel||lbl):lbl}</span></button>;
}
function ProgressNote({part,labels}:{part:Note;labels:ConversationProps['labels']}) {
 const components=useMemo<Components>(()=>({
  a:({href,children})=><a href={publicHref(href)} target="_blank" rel="noopener noreferrer">{children}</a>,
  img:({src,alt})=>{const safe=publicHref(src);return safe?<img src={safe} alt={alt||''} loading="lazy" referrerPolicy="no-referrer"/>:<span>{alt}</span>;},
  table:({children})=><div className="md-table-wrap"><table>{children}</table></div>,
  code:({children,className,node})=>{
   if(node?.position?.start.line===node?.position?.end.line)return <code>{children}</code>;
   const language=/language-([\w+-]+)/.exec(className||'')?.[1]||'plaintext';
   const code=String(children||'').replace(/\n$/,'');
   return <CodeBlock><CodeBlock.Header><span className="code-block__lang"><Icon name="code" size="xs"/><span>{language}</span></span><CopyInline text={code} label={labels.copy} copiedLabel={labels.copied} failedLabel={labels.copyFailed}/></CodeBlock.Header><CodeBlock.Code code={code} language={language}/></CodeBlock>;
  },
 }),[labels.copy,labels.copied,labels.copyFailed]);
 return <ChatMessage.Assistant className="progress-message" data-public-progress={part.source}>
  <ChatMessage.Body><ChatMessage.Content><Markdown id={'progress-'+part.id} components={components}>{part.message}</Markdown></ChatMessage.Content></ChatMessage.Body>
 </ChatMessage.Assistant>;
}
function animateOnce(element:HTMLElement|null,keyframes:Keyframe[],duration:number) {
 const preference=matchMedia('(prefers-reduced-motion: reduce)');
 const animation=preference.matches?undefined:element?.animate(keyframes,{duration,easing:'cubic-bezier(.2,.7,.2,1)'});
 const stopMotion=()=>{if(preference.matches)animation?.cancel();};
 preference.addEventListener('change',stopMotion);
 return()=>{animation?.cancel();preference.removeEventListener('change',stopMotion);};
}
function ProgressParagraph({part,labels}:{part:Note;labels:ConversationProps['labels']}) {
 const element=useRef<HTMLDivElement>(null);
 // Animate only on React mount; SSE can move this same host without replaying.
 useEffect(()=>animateOnce(element.current,[
  {opacity:0,transform:'translateY(5px)'},{opacity:1,transform:'translateY(0)'},
 ],220),[]);
 return <div ref={element} className="progress-note-island"><ProgressNote part={part} labels={labels}/></div>;
}
function PlanView({plan}:{plan:Plan}) {
 if(!plan.steps.length&&!plan.agents.length)return null;
 const doneCount=plan.steps.filter(s=>s.status==='done').length;
 return <div className="execution-plan">
  {plan.steps.length>0&&<div className="plan"><div className="plan-head"><div className="plan-title">{plan.title}</div><span className="plan-progress">{doneCount}/{plan.steps.length}</span></div><ol className="plan-steps">
   {plan.steps.map(step=><li key={step.id} className={'plan-step'+(step.current?' current':'')} data-status={step.status} title={step.statusLabel} aria-label={`${step.title} · ${step.statusLabel}`}><span className="plan-mark">{step.status==='done'&&<Icon name="check" size="xs"/>}</span><span className="plan-text">{step.title}</span></li>)}
  </ol></div>}
  {plan.agents.length>0&&<div className="subagents"><div className="subagents-title">{plan.agentsTitle}</div>{plan.agents.map(agent=><div key={agent.id} className="subagent" data-node={agent.name} data-state={agent.state} title={agent.statusLabel} aria-label={`${agent.name} · ${agent.statusLabel}`}>
   <span className="subagent-mark"/><span className="subagent-name">{agent.name}</span><span className="subagent-steps muted">{agent.label}</span>{agent.last&&<span className="subagent-last">{agent.last}</span>}
  </div>)}</div>}
 </div>;
}
function ToolRow({step,labels}:{step:Step;labels:ConversationProps['labels']}) {
 const previous=useRef(step.state);
 const status=useRef<HTMLSpanElement>(null);
 const [settled,setSettled]=useState(false);
 useEffect(()=>{
  const completed=previous.current==='input-available'&&step.state!=='input-available';
  previous.current=step.state;
  if(!completed)return;
  setSettled(true);
  // An explicit Animation belongs to this state transition, so reparenting the
  // React host during SSE cannot restart it as it can a CSS animation.
  const dispose=animateOnce(status.current,[
   {opacity:.45,transform:'scale(.8)'},
   {opacity:1,transform:'scale(1.12)',offset:.55},
   {opacity:1,transform:'scale(1)'},
  ],280);
  const timer=setTimeout(()=>setSettled(false),320);
  return()=>{clearTimeout(timer);dispose();};
 },[step.state]);
 return <ChatTool className="execution-tool" data-execution-step="" data-call-id={step.id} state={step.state} active={false} toolName={step.tool} defaultExpanded={false}>
  <ChatTool.Trigger aria-label={step.state==='output-error'?`${labels.failed}: ${step.label} ${step.target}`:undefined}>
   <span className="execution-row"><span ref={status} className="execution-status" data-settled={settled}><ChatTool.StatusIcon/></span><span className="execution-label">{step.label}</span>
    {step.target&&<span className="execution-target" title={step.target}>{step.target}</span>}
    {step.duration&&<span className="execution-duration">{step.duration}</span>}
   </span>
  </ChatTool.Trigger>
  <ChatTool.Content>
   {step.target&&<ChatTool.Args label={step.tool==='shell_run'?labels.command:labels.target}><div className="execution-code-wrap"><pre className="execution-code">{step.target}</pre>{labels.copy&&<CopyInline text={step.target} label={labels.copy} copiedLabel={labels.copied} failedLabel={labels.copyFailed} className="btn ghost xs execution-code-copy"/>}</div></ChatTool.Args>}
   {step.error&&<ChatTool.Error label={labels.failed} errorText={step.error}/>}
   {(step.detail||step.time)&&<div className="execution-meta">{step.detail&&<span>{step.detail}</span>}<time>{step.time}</time></div>}
  </ChatTool.Content>
 </ChatTool>;
}
function ToolGroup({part,labels}:{part:Group;labels:ConversationProps['labels']}) {
 const [expanded,setExpanded]=useState(false);
 const [all,setAll]=useState(false);
 useEffect(()=>{if(part.reveal)setExpanded(true);},[part.reveal]);
 // The group follows document scrolling. Large histories disclose more rows,
 // never create another scroll viewport inside the conversation.
 const visible=all?part.steps:part.steps.slice(0,8);
 const failures=part.steps.filter(step=>step.state==='output-error');
 const hiddenFailures=!all&&part.steps.slice(8).some(step=>step.state==='output-error');
 return <div className="progress-island">
  <div className={'worklog pro-worklog'+(expanded?' open':'')} data-pro-component="ChainOfThought" data-running={part.running?'true':undefined}>
   <ChainOfThought isExpanded={expanded} onExpandedChange={setExpanded}>
    <ChainOfThought.Trigger className="worklog-head"><span className="execution-lead-icon" aria-hidden="true"><Icon name={part.running?'sparkles':'terminal'} size="xs"/></span><span className={'execution-summary'+(part.running?' ag-shimmer':'')}>{part.title}</span>
     {failures.length>0&&<span className="execution-failures">{labels.failed} {failures.length}</span>}
    </ChainOfThought.Trigger>
    <ChainOfThought.Content className="execution-content">
     {part.plan&&<PlanView plan={part.plan}/>}
     {part.steps.length>0&&<div className="worklog-steps">{visible.map(step=><ToolRow key={step.id} step={step} labels={labels}/>)}</div>}
     {part.steps.length>8&&<div className="execution-actions"><Button className="execution-more" size="sm" variant="ghost" onPress={()=>setAll(!all)}>{all?labels.less:labels.more.replace('{n}',String(part.steps.length-8))}</Button>{hiddenFailures&&labels.jumpFailed&&<Button className="flow-jump-failed" size="sm" variant="ghost" onPress={()=>setAll(true)}>{labels.jumpFailed}</Button>}</div>}
    </ChainOfThought.Content>
   </ChainOfThought>
   {part.running&&part.current&&<div className="execution-current" role="status"><ChatLoader.Dots size="sm" aria-hidden="true"/><span data-current-label="" className="ag-shimmer">{part.current}</span></div>}
  </div>
 </div>;
}
export function Conversation({parts,labels}:ConversationProps) {
 return <div className="conversation-flow" data-pro-component="Conversation">{parts.map(part=>part.kind==='progress'
  ?<ProgressParagraph key={part.id} part={part} labels={labels}/>
  :<ToolGroup key={part.id} part={part} labels={labels}/>)}</div>;
}
